package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/stt"
)

// demoMeetingSpeakersEnv scripts a multi-voice meeting + speaker analysis
// status progression for VHS (no mic, no native diarization models).
const demoMeetingSpeakersEnv = "SAMANTHA_DEMO_MEETING_SPEAKERS"

func demoMeetingSpeakersEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoMeetingSpeakersEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// multiSpeakerDemoProvider emits alternating speakers as labeled finals so the
// meeting TUI looks like a real multi-person conversation.
type multiSpeakerDemoProvider struct{}

func (multiSpeakerDemoProvider) Available() bool { return true }

func (multiSpeakerDemoProvider) Start(ctx context.Context) (stt.Session, error) {
	ch := make(chan stt.Event, 64)
	go func() {
		defer close(ch)
		send := func(e stt.Event) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- e:
				return true
			}
		}
		turns := []struct {
			speaker string
			text    string
		}{
			{"speaker-1", "welcome everyone to the product marketing sync"},
			{"speaker-2", "thanks — I want to cover launch readiness first"},
			{"speaker-1", "sounds good, we still need creative for the homepage"},
			{"speaker-3", "I can take that — copy is almost final"},
			{"speaker-2", "great, let's lock the budget decision before Friday"},
			{"speaker-1", "agreed — I'll send the summary after this call"},
		}
		if !send(stt.PhaseEvent{Phase: "listening"}) {
			return
		}
		sleepDemo(ctx, 600*time.Millisecond)
		for i, turn := range turns {
			for j := 0; j < 6; j++ {
				if !send(stt.InputLevel{Level: 0.2 + float64(j)*0.1}) {
					return
				}
				if j == 1 && !send(stt.PhaseEvent{Phase: "hearing"}) {
					return
				}
				sleepDemo(ctx, 70*time.Millisecond)
			}
			if !send(stt.PhaseEvent{Phase: "transcribing"}) {
				return
			}
			sleepDemo(ctx, 250*time.Millisecond)
			// Prefix speaker label in the transcript line for VHS readability.
			line := fmt.Sprintf("[%s] %s", turn.speaker, turn.text)
			if !send(stt.FinalTranscript{Text: line}) {
				return
			}
			if !send(stt.PhaseEvent{Phase: "listening"}) {
				return
			}
			// Brief pause between speakers.
			sleepDemo(ctx, 350*time.Millisecond)
			_ = i
		}
		if !send(stt.PhaseEvent{Phase: "listening"}) {
			return
		}
		if !send(stt.Timeout{}) {
			return
		}
	}()
	return &demoSession{ch: ch}, nil
}

type meetingSpeakerStatusMsg struct {
	status meeting.AnalysisStatus
	detail string
}

func demoMeetingSpeakerDeps() (listen.Resetter, stt.Provider) {
	return demoCapture{}, multiSpeakerDemoProvider{}
}

// demoMeetingSpeakerStatusCmds scripts queued → running → complete for VHS.
func demoMeetingSpeakerStatusCmds() tea.Cmd {
	if !demoMeetingSpeakersEnabled() {
		return nil
	}
	return tea.Sequence(
		func() tea.Msg {
			return meetingSpeakerStatusMsg{status: meeting.AnalysisQueued, detail: "multi-voice fixture"}
		},
		tea.Tick(900*time.Millisecond, func(time.Time) tea.Msg {
			return meetingSpeakerStatusMsg{status: meeting.AnalysisRunning, detail: "diarizing…"}
		}),
		tea.Tick(2800*time.Millisecond, func(time.Time) tea.Msg {
			return meetingSpeakerStatusMsg{
				status: meeting.AnalysisComplete,
				detail: "3 speakers · speaker-1/2/3",
			}
		}),
	)
}

// demoMeetingSpeakerFinalizer turns the scripted labels into the same
// post-capture event contract as native diarization, so VHS exercises the real
// results screen without loading a model or microphone.
func demoMeetingSpeakerFinalizer(writer *meetinglog.Writer, bundlePath string) func(context.Context) (meeting.AnalysisResult, error) {
	if writer == nil {
		return nil
	}
	return func(ctx context.Context) (meeting.AnalysisResult, error) {
		if err := ctx.Err(); err != nil {
			return meeting.AnalysisResult{Status: meeting.AnalysisError, Error: err.Error()}, err
		}
		if !strings.HasSuffix(strings.ToLower(filepath.Base(bundlePath)), ".meeting") {
			err := fmt.Errorf("demo meeting: .meeting bundle path is required")
			return meeting.AnalysisResult{Status: meeting.AnalysisError, Error: err.Error()}, err
		}
		analysis := meetinglog.SpeakerAnalysis{Status: string(meeting.AnalysisComplete)}
		result := meeting.AnalysisResult{Status: meeting.AnalysisComplete}
		labels := make(map[string]struct{})
		for i, record := range writer.Transcripts() {
			label, text := splitSpeakerLabel(record.Text)
			if label == "" {
				continue
			}
			labels[label] = struct{}{}
			id := fmt.Sprintf("demo-diarization-%d", i+1)
			result.Timeline.Observations = append(result.Timeline.Observations, speaker.Observation{
				SegmentID: id, StartMS: record.StartMS, EndMS: record.EndMS,
				Label: label, Confidence: 1, State: speaker.StateStable,
				Source: speaker.SourceRecording, ModelRev: "vhs-demo",
			})
			analysis.Segments = append(analysis.Segments, meetinglog.SpeakerSegment{
				ID: id, StartMS: record.StartMS, EndMS: record.EndMS,
				Label: label, Confidence: 1, State: string(speaker.StateStable),
			})
			analysis.Utterances = append(analysis.Utterances, meetinglog.SpeakerUtterance{
				TranscriptRecord: meetinglog.TranscriptRecord{
					ID: record.ID, StartMS: record.StartMS, EndMS: record.EndMS, Text: text,
				},
				Speaker: label, Confidence: 1, State: string(speaker.StateStable),
			})
		}
		result.SpeakerCount = len(labels)
		result.Artifact = filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleSpeakerAnalysisName)
		analysis.Artifact = result.Artifact
		if err := meeting.WriteAnalysis(result.Artifact, result); err != nil {
			return meeting.AnalysisResult{Status: meeting.AnalysisError, Error: err.Error()}, err
		}
		if err := writer.WriteSpeakerAnalysis(analysis); err != nil {
			return meeting.AnalysisResult{Status: meeting.AnalysisError, Error: err.Error()}, err
		}
		return result, nil
	}
}
