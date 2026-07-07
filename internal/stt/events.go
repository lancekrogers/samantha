package stt

import "time"

// EventKind identifies the concrete STT event carried by a TypedEvent.
type EventKind string

const (
	KindPhase             EventKind = "phase"
	KindPartialTranscript EventKind = "partial_transcript"
	KindFinalTranscript   EventKind = "final_transcript"
	KindTimeout           EventKind = "timeout"
	KindFailure           EventKind = "failure"
)

// TypedEvent is a serializable, uniform envelope for STT events. It carries
// error text rather than error values so it can round-trip through encoding;
// consumers that need the original error keep using the source event structs.
type TypedEvent struct {
	Kind       EventKind
	Text       string
	Phase      string
	Final      bool
	ErrText    string
	Confidence float64
	Elapsed    time.Duration
}

// ToTyped converts any STT event into its TypedEvent envelope.
func ToTyped(ev Event) TypedEvent {
	switch e := ev.(type) {
	case PhaseEvent:
		return TypedEvent{Kind: KindPhase, Phase: e.Phase, Elapsed: time.Duration(e.Elapsed)}
	case PartialTranscript:
		return TypedEvent{Kind: KindPartialTranscript, Text: e.Text}
	case FinalTranscript:
		return TypedEvent{Kind: KindFinalTranscript, Text: e.Text, Final: true}
	case Timeout:
		return TypedEvent{Kind: KindTimeout}
	case Failure:
		te := TypedEvent{Kind: KindFailure}
		if e.Err != nil {
			te.ErrText = e.Err.Error()
		}
		return te
	case nil:
		return TypedEvent{}
	default:
		return TypedEvent{Kind: EventKind(ev.sttEvent())}
	}
}
