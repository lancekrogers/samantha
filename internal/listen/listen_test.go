package listen

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/stt"
)

type fakeSession struct{ events []stt.Event }

func (s *fakeSession) Events() <-chan stt.Event {
	ch := make(chan stt.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch
}

func (s *fakeSession) Close() error { return nil }

// fakeProvider returns one scripted session per Start call; when the script
// is exhausted it cancels ctx so Loop stops cleanly.
type fakeProvider struct {
	mu       sync.Mutex
	sessions []*fakeSession
	startErr []error
	starts   int
	resets   int
	cancel   context.CancelFunc
}

func (p *fakeProvider) Start(ctx context.Context) (stt.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.starts
	p.starts++
	if i < len(p.startErr) && p.startErr[i] != nil {
		return nil, p.startErr[i]
	}
	if i >= len(p.sessions) {
		p.cancel()
		return nil, context.Canceled
	}
	return p.sessions[i], nil
}

func (p *fakeProvider) Available() bool { return true }
func (p *fakeProvider) Reset()          { p.mu.Lock(); p.resets++; p.mu.Unlock() }

type recordingSink struct {
	mu         sync.Mutex
	utterances []Utterance
	timeouts   int
	errors     []error
	writeErr   error
}

func (s *recordingSink) OnUtterance(u Utterance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.utterances = append(s.utterances, u)
	return nil
}
func (s *recordingSink) OnTimeout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeouts++
	return s.writeErr
}
func (s *recordingSink) OnError(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
	return s.writeErr
}

func TestLoopDispatchesUtterancesAcrossSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &fakeProvider{
		sessions: []*fakeSession{
			{events: []stt.Event{stt.PartialTranscript{Text: "hel"}, stt.FinalTranscript{Text: "hello there"}}},
			{events: []stt.Event{stt.Timeout{}}},
			{events: []stt.Event{stt.FinalTranscript{Text: "second utterance"}}},
		},
		cancel: cancel,
	}
	sink := &recordingSink{}
	if err := Loop(ctx, p, p, sink); err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if len(sink.utterances) != 2 || sink.utterances[0].Text != "hello there" || sink.utterances[1].Text != "second utterance" {
		t.Fatalf("utterances = %+v", sink.utterances)
	}
	if sink.timeouts != 1 {
		t.Fatalf("timeouts = %d", sink.timeouts)
	}
	if p.resets < 3 {
		t.Fatalf("capture must be reset before each session, resets = %d", p.resets)
	}
}

func TestLoopGivesUpAfterConsecutiveFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fail := errors.New("mic exploded")
	p := &fakeProvider{startErr: []error{fail, fail, fail}, cancel: cancel}
	sink := &recordingSink{}
	err := Loop(ctx, p, p, sink)
	if err == nil {
		t.Fatal("expected failure-threshold error")
	}
	if len(sink.errors) != 3 {
		t.Fatalf("errors reported = %d, want 3", len(sink.errors))
	}
}

func TestLoopFailureCounterResetsOnSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fail := errors.New("transient")
	p := &fakeProvider{
		startErr: []error{fail, fail, nil, fail, fail},
		sessions: []*fakeSession{nil, nil, {events: []stt.Event{stt.FinalTranscript{Text: "recovered"}}}},
		cancel:   cancel,
	}
	sink := &recordingSink{}
	if err := Loop(ctx, p, p, sink); err != nil {
		t.Fatalf("two failures, success, two failures must not trip threshold: %v", err)
	}
	if len(sink.utterances) != 1 {
		t.Fatalf("utterances = %+v", sink.utterances)
	}
}

func TestLoopReturnsNilOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProvider{
		sessions: []*fakeSession{{events: []stt.Event{stt.FinalTranscript{Text: "last words"}}}},
		cancel:   cancel,
	}
	sink := &recordingSink{}
	if err := Loop(ctx, p, p, sink); err != nil {
		t.Fatalf("clean stop must return nil, got %v", err)
	}
}

func TestLoopSessionFailureEventsCountTowardThreshold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failEvents := []stt.Event{stt.Failure{Err: errors.New("decode error")}}
	p := &fakeProvider{
		sessions: []*fakeSession{{events: failEvents}, {events: failEvents}, {events: failEvents}},
		cancel:   cancel,
	}
	sink := &recordingSink{}
	if err := Loop(ctx, p, p, sink); err == nil {
		t.Fatal("three in-session failures must trip the threshold")
	}
}

func TestLoopReturnsSinkFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &fakeProvider{
		sessions: []*fakeSession{{events: []stt.Event{stt.FinalTranscript{Text: "cannot persist"}}}},
		cancel:   cancel,
	}
	sink := &recordingSink{writeErr: errors.New("disk full")}
	err := Loop(ctx, p, p, sink)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Loop error = %v, want sink failure", err)
	}
}

func TestDrainSessionTreatsContextCancellationAsCleanStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := &fakeSession{events: []stt.Event{stt.Failure{Err: context.Canceled}}}
	sink := &recordingSink{}
	failures := 0
	failed, err := drainSession(ctx, session, sink, Hooks{}, &failures)
	if err != nil || failed {
		t.Fatalf("drainSession = (%v, %v), want clean stop", failed, err)
	}
	if failures != 0 || len(sink.errors) != 0 {
		t.Fatalf("cancellation recorded as failure: failures=%d errors=%v", failures, sink.errors)
	}
}

func TestLoopWithHooksReceivesPhaseLevelPartial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &fakeProvider{
		sessions: []*fakeSession{{
			events: []stt.Event{
				stt.PhaseEvent{Phase: "listening"},
				stt.InputLevel{Level: 0.7},
				stt.PhaseEvent{Phase: "hearing"},
				stt.PartialTranscript{Text: "hel"},
				stt.FinalTranscript{Text: "hello"},
			},
		}},
		cancel: cancel,
	}
	sink := &recordingSink{}
	var phases []string
	var levels []float64
	var partials []string
	hooks := Hooks{
		OnPhase:   func(p string) { phases = append(phases, p) },
		OnLevel:   func(l float64) { levels = append(levels, l) },
		OnPartial: func(t string) { partials = append(partials, t) },
	}
	if err := LoopWithHooks(ctx, p, p, sink, hooks); err != nil {
		t.Fatalf("LoopWithHooks: %v", err)
	}
	if len(sink.utterances) != 1 || sink.utterances[0].Text != "hello" {
		t.Fatalf("utterances = %+v", sink.utterances)
	}
	if len(phases) < 2 || phases[0] != "listening" {
		t.Fatalf("phases = %v", phases)
	}
	if len(levels) != 1 || levels[0] != 0.7 {
		t.Fatalf("levels = %v", levels)
	}
	if len(partials) != 1 || partials[0] != "hel" {
		t.Fatalf("partials = %v", partials)
	}
}
