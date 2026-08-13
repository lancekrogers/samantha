package voiceagent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// EventStream is a buffered, non-blocking view of the agent's event bus.
//
// The bus itself is **synchronous**: SubscribeAll invokes handlers inline on the
// goroutine that emitted the event, which is the audio pipeline. Handing that to
// an embedder means a host that is slow to read — rendering a frame, writing to a
// socket — stalls speech synthesis. So this adapter buffers, and when the buffer
// is full it **drops** rather than blocking.
//
// Dropping is the right trade for telemetry, but a silent drop is
// indistinguishable from a bug when a host notices missing events. Dropped()
// exists so the host can tell the difference, and is the first thing to check
// when events seem to be missing.
//
// Power users who want every event and are willing to honour the synchronous
// contract can still subscribe to the Bus directly.
type EventStream struct {
	ch      chan events.Event
	dropped atomic.Uint64
	once    sync.Once
	remove  func()
}

// EventStream returns a buffered stream of every event the agent emits.
//
// Named EventStream, not Events: Agent embeds *pipeline.Pipeline, whose Events
// field is the bus itself. A method of the same name would shadow that field and
// leave `agent.Events` meaning something different depending on how it is
// spelled — exactly the kind of surface that reads fine and confuses everyone.
//
// The caller must Close the stream when finished, or the subscription outlives
// it. buffer is the number of events held before dropping starts; a buffer of
// zero or less gets a small default rather than an unbuffered channel, because
// an unbuffered channel here would drop all but perfectly-timed reads.
func (a *Agent) EventStream(buffer int) *EventStream {
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}
	s := &EventStream{ch: make(chan events.Event, buffer)}
	s.remove = a.Pipeline.Events.SubscribeAll(func(e events.Event) {
		select {
		case s.ch <- e:
		default:
			// Full: drop and count. Never block — this runs inline on the
			// pipeline's goroutine.
			s.dropped.Add(1)
		}
	})
	return s
}

const defaultEventBuffer = 256

// C is the channel to range over. It is closed by Close.
func (s *EventStream) C() <-chan events.Event { return s.ch }

// Dropped reports how many events were discarded because the buffer was full.
// A non-zero value means this consumer is slower than the pipeline, not that the
// pipeline misbehaved.
func (s *EventStream) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes and closes the channel. Safe to call more than once.
func (s *EventStream) Close() {
	s.once.Do(func() {
		if s.remove != nil {
			s.remove()
		}
		close(s.ch)
	})
}

// Interrupt cancels the turn currently in flight, if any.
//
// Safe with no turn running and safe to call repeatedly — both are ordinary
// states for a host wired to a button or a hotkey, not errors.
func (a *Agent) Interrupt() {
	a.mu.Lock()
	cancel := a.cancelTurn
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SendText runs one text turn and returns when the agent has finished responding.
// The turn is cancellable through Interrupt for its duration.
func (a *Agent) SendText(ctx context.Context, input string) error {
	turnCtx, cancel := context.WithCancel(ctx)

	a.mu.Lock()
	a.turnSeq++
	seq := a.turnSeq
	a.cancelTurn = cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		// Clear only if this turn is still the registered one. Comparing a
		// sequence number rather than the func — funcs are not comparable, and a
		// later turn overwriting this one must not have its cancel dropped, or
		// Interrupt silently stops working.
		if a.turnSeq == seq {
			a.cancelTurn = nil
		}
		a.mu.Unlock()
	}()

	return a.Pipeline.RunTurnTextMode(turnCtx, input)
}
