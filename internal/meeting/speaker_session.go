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

// workingAudioPartName is the transient streamed-PCM file used when the meeting
// is diarized but audio retention (record_audio) is off. It lives inside the
// bundle's hidden internal dir and is removed once diarization has read it.
const workingAudioPartName = "audio.wav.part"

type captureSubscriber interface {
	Subscribe(int) (int, <-chan []float32)
	Unsubscribe(int)
}

// SpeakerSession owns the optional meeting PCM subscriber and post-capture
// analysis. It never runs model work on the capture callback or STT goroutine.
//
// Captured PCM is streamed straight to a WAV file on disk rather than buffered
// in memory for the whole meeting: a multi-hour recording keeps process memory
// flat, a crash leaves a valid partial WAV, and diarization re-reads the file at
// Finalize instead of retaining a second full copy.
type SpeakerSession struct {
	capture     captureSubscriber
	subID       int
	analyzer    SpeakerAnalyzer
	closer      interface{ Close() error }
	writer      *meetinglog.Writer
	bundlePath  string
	recordAudio bool

	// audioWriter streams capture chunks to workingPath. workingPath is the
	// final bundle audio.wav when record_audio is on, otherwise a transient
	// .part file that Finalize/Close remove after diarization.
	audioWriter *audio.WAVWriter
	workingPath string
	writeErr    error

	stop        sync.Once
	wg          sync.WaitGroup
	discardOnce sync.Once

	finalize sync.Once
	result   AnalysisResult
	err      error
}

func NewSpeakerSession(capture captureSubscriber, analyzer SpeakerAnalyzer, writer *meetinglog.Writer, bundlePath string, recordAudio bool) (*SpeakerSession, error) {
	if capture == nil {
		return nil, fmt.Errorf("meeting: speaker capture is required")
	}
	if analyzer == nil {
		return nil, fmt.Errorf("meeting: speaker analyzer is required")
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(bundlePath))), ".meeting") {
		return nil, fmt.Errorf("meeting: .meeting bundle path is required for speaker analysis")
	}

	workingPath := speakerAudioPath(bundlePath)
	if !recordAudio {
		workingPath = filepath.Join(bundlePath, meetinglog.BundleInternalDirName, workingAudioPartName)
	}
	if err := os.MkdirAll(filepath.Dir(workingPath), 0o700); err != nil {
		return nil, fmt.Errorf("meeting: prepare speaker audio dir: %w", err)
	}
	audioWriter, err := audio.NewWAVWriter(workingPath, audio.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("meeting: open speaker audio file: %w", err)
	}

	id, chunks := capture.Subscribe(speakerCaptureQueue)
	s := &SpeakerSession{
		capture: capture, subID: id, analyzer: analyzer, writer: writer,
		bundlePath: bundlePath, recordAudio: recordAudio,
		audioWriter: audioWriter, workingPath: workingPath,
	}
	if closer, ok := analyzer.(interface{ Close() error }); ok {
		s.closer = closer
	}
	s.wg.Go(func() {
		for chunk := range chunks {
			if s.writeErr != nil {
				continue // drain the subscription but stop writing after an error
			}
			if err := s.audioWriter.Write(chunk); err != nil {
				s.writeErr = err
			}
		}
	})
	return s, nil
}

// stopCapture unsubscribes, joins the writer goroutine, and closes the streamed
// WAV so its size fields are finalized before any re-read. Idempotent.
func (s *SpeakerSession) stopCapture() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		s.capture.Unsubscribe(s.subID)
		s.wg.Wait()
		if s.audioWriter != nil {
			if err := s.audioWriter.Close(); err != nil && s.writeErr == nil {
				s.writeErr = err
			}
		}
	})
}

// Finalize stops PCM collection, runs offline diarization, and persists an
// additive analysis sidecar plus human/JSONL log records. It is idempotent.
func (s *SpeakerSession) Finalize(ctx context.Context) (AnalysisResult, error) {
	if s == nil {
		return AnalysisResult{Status: AnalysisDisabled}, nil
	}
	s.finalize.Do(func() {
		s.stopCapture()
		artifact := speakerArtifactPath(s.bundlePath)
		var persistErr error
		if s.writeErr != nil {
			persistErr = errors.Join(persistErr, fmt.Errorf("record meeting audio: %w", s.writeErr))
		}

		// Re-read the streamed PCM for offline diarization instead of holding the
		// whole recording in memory for the meeting's duration.
		var samples []float32
		if s.audioWriter != nil && s.audioWriter.Samples() > 0 {
			got, _, rerr := audio.ReadWAVFloat32(s.workingPath)
			if rerr != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("read meeting audio: %w", rerr))
			} else {
				samples = got
			}
		}

		audioFile := ""
		if s.recordAudio {
			audioFile = s.workingPath // the streamed bundle audio.wav
			if err := os.Chmod(audioFile, 0o600); err != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("secure meeting audio: %w", err))
			}
		} else {
			// record_audio is off: keep the diarization result, not the audio.
			s.discardWorkingAudio()
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

// discardWorkingAudio removes the streamed PCM when audio retention is off.
// Safe to call from both Finalize and Close.
func (s *SpeakerSession) discardWorkingAudio() {
	if s == nil || s.recordAudio || s.workingPath == "" {
		return
	}
	s.discardOnce.Do(func() { _ = os.Remove(s.workingPath) })
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

func speakerArtifactPath(bundlePath string) string {
	return filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleSpeakerAnalysisName)
}

func speakerAudioPath(bundlePath string) string {
	return filepath.Join(bundlePath, meetinglog.BundleAudioName)
}

// Close stops collection and releases native resources when a meeting exits
// before normal finalization.
func (s *SpeakerSession) Close() error {
	if s == nil {
		return nil
	}
	s.stopCapture()
	// If the meeting aborted before Finalize and audio was not opted in, do not
	// leave the streamed PCM behind.
	s.discardWorkingAudio()
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
