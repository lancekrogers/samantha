package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/stt"
)

// ConversationRuntime is a live pipeline prepared for the conversation
// screen. The TUI owns its lifecycle from the moment the builder returns it:
// Cleanup runs exactly once, after the program exits and in-flight turns
// have drained.
type ConversationRuntime struct {
	Pipeline *pipeline.Pipeline
	Bus      *events.Bus
	Voice    bool // STT is configured; voice turns may run
	Output   bool // TTS and playback are configured
	// PersonaID and AgentName identify the session binding this runtime was
	// built from. The conversation renders this identity for its lifetime —
	// later persona switches or edits must not relabel an in-flight session.
	PersonaID    string
	AgentName    string
	SessionID    string
	InputDevice  string
	OutputDevice string
	LiveSpeaker  LiveSpeakerController
	Seed         []brain.Turn                // resumed history to pre-populate the viewport
	ReloadVoice  func(context.Context) error // applies Settings to subsequent utterances
	Cleanup      func()                      // tears down pipeline resources and saves the session
}

// RuntimeBuilder constructs the runtime when the user enters the
// conversation screen (D2: the mic goes hot here, not in the launcher).
// Asset download progress is reported through progress and rendered
// in-screen. personaID selects the session's persona binding; empty resolves
// the configured active persona.
type RuntimeBuilder func(ctx context.Context, progress func(name string, pct float64), sessionID, personaID string) (*ConversationRuntime, error)

// MeetingRuntime holds STT, the bundle writer, and optional speaker finalizer
// for the embedded meeting screen.
type MeetingRuntime struct {
	Capture          listen.Resetter
	Provider         stt.Provider
	Writer           *meetinglog.Writer
	FinalizeSpeakers func(context.Context) (meeting.AnalysisResult, error)
	SpeakerStatus    meeting.AnalysisStatus
	SpeakerError     string
	Description      string
	Path             string
	StopPhrases      map[string]bool
	Cleanup          func()
}

// MeetingBuilder constructs meeting resources when the user starts recording
// from the main launcher (assets + mic hot only after title confirm).
type MeetingBuilder func(ctx context.Context, description string, progress func(name string, pct float64)) (*MeetingRuntime, error)

// meetingReadyMsg delivers a built meeting runtime (or error) into Update.
type meetingReadyMsg struct {
	rt  *MeetingRuntime
	err error
}

func buildMeeting(build MeetingBuilder, ctx context.Context, description string) tea.Cmd {
	if build == nil {
		return func() tea.Msg {
			return meetingReadyMsg{err: fmt.Errorf("meeting builder not configured")}
		}
	}
	return func() tea.Msg {
		rt, err := build(ctx, description, func(name string, pct float64) {
			// Progress is best-effort; the preparing screen shows a static label.
			_ = name
			_ = pct
		})
		return meetingReadyMsg{rt: rt, err: err}
	}
}

// runtimeSlot owns the built ConversationRuntime across the race between the
// builder Cmd finishing and the Bubble Tea program quitting. Without it, a
// runtimeReadyMsg that never reaches Update would leak mic/player resources.
type runtimeSlot struct {
	mu sync.Mutex
	rt *ConversationRuntime
}

func (s *runtimeSlot) store(rt *ConversationRuntime) {
	s.mu.Lock()
	old := s.rt
	s.rt = rt
	s.mu.Unlock()
	if old != nil && old != rt && old.Cleanup != nil {
		old.Cleanup()
	}
}

func (s *runtimeSlot) cleanup() {
	s.mu.Lock()
	rt := s.rt
	s.rt = nil
	s.mu.Unlock()
	if rt != nil && rt.Cleanup != nil {
		rt.Cleanup()
	}
}

// assetProgressMsg carries EnsureRuntimeAssets progress into the update loop.
type assetProgressMsg struct {
	name string
	pct  float64
}

// progressClosedMsg tells the update loop to stop draining the progress feed.
type progressClosedMsg struct{}

// runtimeReadyMsg delivers the built runtime (or the fatal build error).
type runtimeReadyMsg struct {
	rt  *ConversationRuntime
	err error
}

type voiceReloadedMsg struct {
	err         error
	resumeVoice bool
}

func reloadVoice(ctx context.Context, reload func(context.Context) error, resumeVoice bool) tea.Cmd {
	return func() tea.Msg {
		return voiceReloadedMsg{err: reload(ctx), resumeVoice: resumeVoice}
	}
}

// buildRuntime runs the builder off the update loop, streaming progress
// through the feed so the conversation screen can render it.
func buildRuntime(build RuntimeBuilder, ctx context.Context, feed *eventBridge, slot *runtimeSlot, sessionID, personaID string) tea.Cmd {
	return func() tea.Msg {
		rt, err := build(ctx, func(name string, pct float64) {
			feed.send(assetProgressMsg{name: name, pct: pct})
		}, sessionID, personaID)
		feed.send(progressClosedMsg{})
		if err != nil {
			return runtimeReadyMsg{err: err}
		}
		if rt != nil && slot != nil {
			// Claim ownership before returning so run() can always clean up,
			// even if the ready message is never applied (quit mid-build).
			slot.store(rt)
			if ctx.Err() != nil {
				slot.cleanup()
				return runtimeReadyMsg{err: ctx.Err()}
			}
		}
		return runtimeReadyMsg{rt: rt, err: err}
	}
}

func formatAssetProgress(msg assetProgressMsg) string {
	if msg.pct <= 0 {
		return fmt.Sprintf("Downloading %s...", msg.name)
	}
	return fmt.Sprintf("Downloading %s: %d%%", msg.name, int(msg.pct))
}

// drainTimeout caps how long shutdown waits for an in-flight turn after its
// context is canceled, mirroring cmd/samantha's forceQuitTimeout.
const drainTimeout = 3 * time.Second

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
