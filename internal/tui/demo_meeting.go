package tui

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/stt"
)

// demoMeetingEnv scripts mic phases/levels for VHS without hardware.
const demoMeetingEnv = "SAMANTHA_DEMO_MEETING"

func demoMeetingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(demoMeetingEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// demoCapture satisfies listen.Resetter with no hardware.
type demoCapture struct{}

func (demoCapture) Reset() {}

// demoProvider runs one scripted STT session story, then silent timeouts until
// the context is cancelled (user stops the recorder).
type demoProvider struct{}

func (demoProvider) Available() bool { return true }

func (demoProvider) Start(ctx context.Context) (stt.Session, error) {
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
		// Opening listen
		if !send(stt.PhaseEvent{Phase: "listening"}) {
			return
		}
		sleepDemo(ctx, 800*time.Millisecond)
		// Rising energy → hearing
		for i := 0; i < 12; i++ {
			if !send(stt.InputLevel{Level: 0.15 + float64(i)*0.06}) {
				return
			}
			if i == 2 && !send(stt.PhaseEvent{Phase: "hearing"}) {
				return
			}
			if i == 6 && !send(stt.PartialTranscript{Text: "let's review the roadmap"}) {
				return
			}
			sleepDemo(ctx, 90*time.Millisecond)
		}
		if !send(stt.PhaseEvent{Phase: "transcribing"}) {
			return
		}
		sleepDemo(ctx, 400*time.Millisecond)
		if !send(stt.FinalTranscript{Text: "let's review the roadmap for Q3"}) {
			return
		}
		// Second utterance
		if !send(stt.PhaseEvent{Phase: "listening"}) {
			return
		}
		sleepDemo(ctx, 600*time.Millisecond)
		for i := 0; i < 8; i++ {
			if !send(stt.InputLevel{Level: 0.25 + float64(i)*0.08}) {
				return
			}
			if i == 1 && !send(stt.PhaseEvent{Phase: "hearing"}) {
				return
			}
			sleepDemo(ctx, 80*time.Millisecond)
		}
		if !send(stt.FinalTranscript{Text: "we need a decision on the budget today"}) {
			return
		}
		// Hold the session open with silence timeouts so the UI stays live.
		for {
			if !send(stt.PhaseEvent{Phase: "listening"}) {
				return
			}
			if !send(stt.Timeout{}) {
				return
			}
			// Loop restarts a new session after Timeout return; one Timeout ends this session.
			return
		}
	}()
	return &demoSession{ch: ch}, nil
}

type demoSession struct {
	ch <-chan stt.Event
}

func (s *demoSession) Events() <-chan stt.Event { return s.ch }
func (s *demoSession) Close() error             { return nil }

func sleepDemo(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// demoMeetingDeps returns capture/provider fakes for the meeting builder path.
func demoMeetingDeps() (listen.Resetter, stt.Provider) {
	return demoCapture{}, demoProvider{}
}
