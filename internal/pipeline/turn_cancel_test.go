package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/stt"
)

// D1 spike (samantha-conversation-tui): the TUI cancels a listening voice
// turn when the user submits text. A canceled RunTurn must close its STT
// session cleanly and leave the pipeline ready to run the next turn.
func TestRunTurnCancelWhileListeningThenNextTurn(t *testing.T) {
	bus := events.NewBus()
	var outcomes []string
	events.Subscribe(bus, func(e events.TurnMetrics) { outcomes = append(outcomes, e.Outcome) })

	fake := &cancelSpikeSTT{listening: make(chan struct{})}
	p := &Pipeline{
		STT:    fake,
		Brain:  &fakeBrain{chunks: []string{"hello to you too."}},
		Events: bus,
	}

	ctx, cancel := context.WithCancel(context.Background())
	type turnResult struct {
		text string
		err  error
	}
	result := make(chan turnResult, 1)
	go func() {
		text, err := p.RunTurn(ctx)
		result <- turnResult{text, err}
	}()

	select {
	case <-fake.listening:
	case <-time.After(2 * time.Second):
		t.Fatal("first STT session never started listening")
	}
	cancel()

	var first turnResult
	select {
	case first = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled RunTurn did not return")
	}
	if !errors.Is(first.err, context.Canceled) {
		t.Fatalf("canceled RunTurn error = %v, want context.Canceled", first.err)
	}
	if !fake.session(0).closed.Load() {
		t.Fatal("canceled turn left its STT session open")
	}

	// The next turn must start and complete normally on the same pipeline.
	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("turn after cancel: RunTurn() error = %v", err)
	}
	if text != "hello again" {
		t.Fatalf("turn after cancel: transcript = %q, want %q", text, "hello again")
	}
	if !fake.session(1).closed.Load() {
		t.Fatal("second STT session not closed after its turn completed")
	}
	if len(outcomes) != 2 || outcomes[0] != "interrupted" || outcomes[1] != "completed" {
		t.Fatalf("turn outcomes = %v, want [interrupted completed]", outcomes)
	}
}

// cancelSpikeSTT stalls its first session in the listening state and lets the
// second complete with a transcript, tracking session closes.
type cancelSpikeSTT struct {
	mu        sync.Mutex
	sessions  []*spikeSession
	listening chan struct{}
}

type spikeSession struct {
	events chan stt.Event
	closed atomic.Bool
}

func (s *spikeSession) Events() <-chan stt.Event { return s.events }
func (s *spikeSession) Close() error             { s.closed.Store(true); return nil }

func (f *cancelSpikeSTT) Start(ctx context.Context) (stt.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ch := make(chan stt.Event, 3)
	ch <- stt.PhaseEvent{Phase: "listening"}
	sess := &spikeSession{events: ch}

	if len(f.sessions) == 0 {
		close(f.listening) // open and silent until ctx cancellation
	} else {
		ch <- stt.FinalTranscript{Text: "hello again"}
		close(ch)
	}
	f.sessions = append(f.sessions, sess)
	return sess, nil
}

func (f *cancelSpikeSTT) Available() bool { return true }

func (f *cancelSpikeSTT) session(i int) *spikeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[i]
}
