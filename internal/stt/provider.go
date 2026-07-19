package stt

import "context"

// Event is the base interface for STT session events.
type Event interface {
	sttEvent() string
}

// PhaseEvent reports a phase transition inside the STT session.
type PhaseEvent struct {
	Phase   string
	Elapsed int64 // nanoseconds spent in the previous phase
}

func (e PhaseEvent) sttEvent() string { return "phase" }

// PartialTranscript reports an incremental transcript update.
type PartialTranscript struct {
	Text string
}

func (e PartialTranscript) sttEvent() string { return "partial_transcript" }

// FinalTranscript reports the committed transcript for the turn.
type FinalTranscript struct {
	Text string
}

func (e FinalTranscript) sttEvent() string { return "final_transcript" }

// Timeout reports that no speech was detected before the listen timeout.
type Timeout struct{}

func (e Timeout) sttEvent() string { return "timeout" }

// Failure reports an STT session error.
type Failure struct {
	Err error
}

func (e Failure) sttEvent() string { return "failure" }

// InputLevel is a throttled mic energy sample for UI meters (0..1).
// It is advisory and droppable — loops must never block on delivery.
type InputLevel struct {
	Level float64
}

func (e InputLevel) sttEvent() string { return "input_level" }

// Session produces STT events for one conversational turn.
type Session interface {
	Events() <-chan Event
	Close() error
}

// Provider is the interface all STT backends implement.
type Provider interface {
	// Start begins a session that emits STT events until the turn resolves.
	Start(ctx context.Context) (Session, error)

	// Available returns true if this provider is ready.
	Available() bool
}
