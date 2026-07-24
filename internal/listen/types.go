// Package listen provides the shared event types for continuous listening.
// The native STT loop is excluded from CGO-free integration builds, while
// persistence and routing code can continue to use these boundary types.
package listen

import "time"

// Utterance is one committed transcript segment from a continuous listening
// session, along with when it was captured.
type Utterance struct {
	Text string
	At   time.Time
}

// Sink receives events from a Loop. Implementations must not block for long —
// Loop calls these synchronously between STT events — and should return any
// persistence or output error so recording can stop before more data is lost.
type Sink interface {
	OnUtterance(Utterance) error
	OnTimeout() error        // no speech heard within the STT provider's window
	OnError(err error) error // a session failed; Loop is about to retry or give up
}

// Resetter clears buffered audio before a new session starts. *audio.Capture
// satisfies it; tests inject fakes.
type Resetter interface {
	Reset()
}

// Hooks are optional, droppable UI callbacks for live meters and partials.
// They must never block: STT runs on the loop goroutine and a slow hook would
// stall capture. Nil fields are no-ops.
type Hooks struct {
	OnPhase   func(phase string)  // "listening", "hearing", "transcribing"
	OnLevel   func(level float64) // mic energy 0..1 (throttled by STT)
	OnPartial func(text string)   // incremental transcript
}
