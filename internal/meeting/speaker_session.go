package meeting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lancekrogers/samantha/internal/audio"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
)

const speakerCaptureQueue = 1024

type captureSubscriber interface {
	Subscribe(int) (int, <-chan []float32)
	Unsubscribe(int)
}

// SpeakerSession owns the optional meeting PCM subscriber and post-capture
// analysis. It never runs model work on the capture callback or STT goroutine.
type SpeakerSession struct {
	capture     captureSubscriber
	subID       int
	analyzer    SpeakerAnalyzer
	closer      interface{ Close() error }
	writer      *meetinglog.Writer
	meetingPath string
	recordAudio bool

	mu      sync.Mutex
	samples []float32
	stop    sync.Once
	wg      sync.WaitGroup

	finalize sync.Once
	result   AnalysisResult
	err      error
}

func NewSpeakerSession(capture captureSubscriber, analyzer SpeakerAnalyzer, writer *meetinglog.Writer, meetingPath string, recordAudio bool) (*SpeakerSession, error) {
	if capture == nil {
		return nil, fmt.Errorf("meeting: speaker capture is required")
	}
	if analyzer == nil {
		return nil, fmt.Errorf("meeting: speaker analyzer is required")
	}
	if strings.TrimSpace(meetingPath) == "" {
		return nil, fmt.Errorf("meeting: path is required for speaker analysis")
	}
	id, chunks := capture.Subscribe(speakerCaptureQueue)
	s := &SpeakerSession{
		capture: capture, subID: id, analyzer: analyzer, writer: writer,
		meetingPath: meetingPath, recordAudio: recordAudio,
	}
	if closer, ok := analyzer.(interface{ Close() error }); ok {
		s.closer = closer
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for chunk := range chunks {
			s.mu.Lock()
			s.samples = append(s.samples, chunk...)
			s.mu.Unlock()
		}
	}()
	return s, nil
}

// CapturedSamples returns a copy for diagnostics and tests.
func (s *SpeakerSession) CapturedSamples() []float32 {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float32(nil), s.samples...)
}

func (s *SpeakerSession) stopCapture() []float32 {
	if s == nil {
		return nil
	}
	s.stop.Do(func() {
		s.capture.Unsubscribe(s.subID)
		s.wg.Wait()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := s.samples
	s.samples = nil
	return samples
}

// Finalize stops PCM collection, runs offline diarization, and persists an
// additive analysis sidecar plus human/JSONL log records. It is idempotent.
func (s *SpeakerSession) Finalize(ctx context.Context) (AnalysisResult, error) {
	if s == nil {
		return AnalysisResult{Status: AnalysisDisabled}, nil
	}
	s.finalize.Do(func() {
		samples := s.stopCapture()
		artifact := speakerArtifactPath(s.meetingPath)
		audioFile := ""
		var persistErr error

		if s.recordAudio {
			audioFile = speakerAudioPath(s.meetingPath)
			if err := audio.WriteWAVFloat32(audioFile, audio.SampleRate, samples); err != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("write meeting audio: %w", err))
			} else if err := os.Chmod(audioFile, 0o600); err != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("secure meeting audio: %w", err))
			}
		}

		result := AnalyzeRecording(ctx, s.analyzer, samples)
		result.Artifact = artifact
		result.AudioFile = audioFile
		result.SpeakerCount = distinctSpeakerCount(result.Timeline)
		if persistErr != nil {
			result.Error = joinErrorText(result.Error, persistErr)
			if result.Status == AnalysisComplete {
				result.Status = AnalysisError
			}
		}
		if err := WriteAnalysis(artifact, result); err != nil {
			persistErr = errors.Join(persistErr, err)
			result.Error = joinErrorText(result.Error, err)
			if result.Status == AnalysisComplete {
				result.Status = AnalysisError
			}
		}

		if s.writer != nil {
			analysis := logAnalysis(result, s.writer.Transcripts())
			if err := s.writer.WriteSpeakerAnalysis(analysis); err != nil {
				persistErr = errors.Join(persistErr, err)
				result.Error = joinErrorText(result.Error, err)
				if result.Status == AnalysisComplete {
					result.Status = AnalysisError
				}
			}
		}
		s.result, s.err = result, persistErr
		if s.closer != nil {
			s.err = errors.Join(s.err, s.closer.Close())
		}
	})
	return s.result, s.err
}

func joinErrorText(existing string, err error) string {
	if err == nil {
		return existing
	}
	if existing == "" {
		return err.Error()
	}
	return errors.Join(errors.New(existing), err).Error()
}

func logAnalysis(result AnalysisResult, transcripts []meetinglog.TranscriptRecord) meetinglog.SpeakerAnalysis {
	analysis := meetinglog.SpeakerAnalysis{
		Status: string(result.Status), Error: result.Error,
		Artifact: result.Artifact, AudioFile: result.AudioFile,
	}
	for _, obs := range result.Timeline.Observations {
		analysis.Segments = append(analysis.Segments, meetinglog.SpeakerSegment{
			ID: obs.SegmentID, StartMS: obs.StartMS, EndMS: obs.EndMS,
			Label: obs.Label, Confidence: obs.Confidence, State: string(obs.State),
		})
	}
	segments := make([]TranscriptSegment, 0, len(transcripts))
	for _, transcript := range transcripts {
		segments = append(segments, TranscriptSegment{
			ID: transcript.ID, StartMS: transcript.StartMS,
			EndMS: transcript.EndMS, Text: transcript.Text,
		})
	}
	for _, attributed := range AttributeTranscript(segments, result.Timeline) {
		analysis.Utterances = append(analysis.Utterances, meetinglog.SpeakerUtterance{
			TranscriptRecord: meetinglog.TranscriptRecord{
				ID: attributed.ID, StartMS: attributed.StartMS,
				EndMS: attributed.EndMS, Text: attributed.Text,
			},
			Speaker: attributed.Speaker, Confidence: attributed.Confidence, State: string(attributed.State),
		})
	}
	return analysis
}

func distinctSpeakerCount(timeline speaker.Timeline) int {
	labels := make(map[string]struct{})
	for _, observation := range timeline.Observations {
		if observation.Label != "" && observation.Label != speaker.LabelUnknown {
			labels[observation.Label] = struct{}{}
		}
	}
	return len(labels)
}

func speakerArtifactPath(meetingPath string) string {
	ext := filepath.Ext(meetingPath)
	return strings.TrimSuffix(meetingPath, ext) + ".speaker-analysis.json"
}

func speakerAudioPath(meetingPath string) string {
	ext := filepath.Ext(meetingPath)
	return strings.TrimSuffix(meetingPath, ext) + ".wav"
}

// Close stops collection and releases native resources when a meeting exits
// before normal finalization.
func (s *SpeakerSession) Close() error {
	if s == nil {
		return nil
	}
	s.stopCapture()
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
