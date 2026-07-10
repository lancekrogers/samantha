package listen

import (
	"context"
	"errors"
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
}

func (s *recordingSink) OnUtterance(u Utterance) {
	s.mu.Lock()
	s.utterances = append(s.utterances, u)
	s.mu.Unlock()
}
func (s *recordingSink) OnTimeout() { s.mu.Lock(); s.timeouts++; s.mu.Unlock() }
func (s *recordingSink) OnError(err error) {
	s.mu.Lock()
	s.errors = append(s.errors, err)
	s.mu.Unlock()
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
