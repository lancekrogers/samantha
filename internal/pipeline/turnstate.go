package pipeline

import (
	"slices"
)

// TurnState is an explicit lifecycle state for one interactive voice turn.
//
// The turn state machine (turnMachine) is the single owner of these
// transitions: pipeline stages report facts and the machine decides state. The
// model is intentionally small so it can be exercised without live providers.
//
// Lifecycle (terminal states marked *):
//
//	idle -> listening -> transcribing -> thinking -> speaking -> completed*
//	         |              |               |           |
//	         |              |               |           +-> interrupted*
//	         |              |               +-> completed* (text reply, no TTS)
//	         |              |               +-> failed*    (brain error)
//	         |              +-> timed_out*  (empty transcript)
//	         +-> timed_out* (no speech) / failed* (stt error) / interrupted*
//	idle -> thinking (text mode entry, no microphone)
type TurnState int

const (
	// TurnIdle is the zero value: a turn that has not started yet.
	TurnIdle TurnState = iota
	// TurnListening is waiting for and capturing user speech.
	TurnListening
	// TurnTranscribing is converting captured speech to text.
	TurnTranscribing
	// TurnThinking is waiting on the brain model for a response.
	TurnThinking
	// TurnSpeaking is synthesizing and playing the response.
	TurnSpeaking
	// TurnInterrupted is terminal: the user barged in or canceled the turn.
	TurnInterrupted
	// TurnCompleted is terminal: the turn finished normally.
	TurnCompleted
	// TurnFailed is terminal: a provider, command, asset, or stage errored.
	TurnFailed
	// TurnTimedOut is terminal: no usable speech was captured.
	TurnTimedOut
)

func (s TurnState) String() string {
	switch s {
	case TurnIdle:
		return "idle"
	case TurnListening:
		return "listening"
	case TurnTranscribing:
		return "transcribing"
	case TurnThinking:
		return "thinking"
	case TurnSpeaking:
		return "speaking"
	case TurnInterrupted:
		return "interrupted"
	case TurnCompleted:
		return "completed"
	case TurnFailed:
		return "failed"
	case TurnTimedOut:
		return "timed_out"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether s is a final outcome with no legal successor.
func (s TurnState) IsTerminal() bool {
	switch s {
	case TurnInterrupted, TurnCompleted, TurnFailed, TurnTimedOut:
		return true
	default:
		return false
	}
}

// turnTransitions is the legal transition graph. A state absent as a key (every
// terminal state) has no outgoing transitions.
var turnTransitions = map[TurnState][]TurnState{
	TurnIdle:         {TurnListening, TurnThinking},
	TurnListening:    {TurnTranscribing, TurnThinking, TurnTimedOut, TurnFailed, TurnInterrupted},
	TurnTranscribing: {TurnThinking, TurnTimedOut, TurnFailed, TurnInterrupted},
	TurnThinking:     {TurnSpeaking, TurnCompleted, TurnFailed, TurnInterrupted},
	TurnSpeaking:     {TurnCompleted, TurnFailed, TurnInterrupted},
}

// CanTransitionTo reports whether moving from s to next is legal. Re-entering
// the same state is not a transition and is reported false.
func (s TurnState) CanTransitionTo(next TurnState) bool {
	return slices.Contains(turnTransitions[s], next)
}

// turnMachine tracks the lifecycle state of a single turn and rejects illegal
// transitions, so a late, duplicate, or out-of-order signal cannot corrupt the
// turn's outcome.
type turnMachine struct {
	state TurnState
}

func newTurnMachine() *turnMachine {
	return &turnMachine{state: TurnIdle}
}

// State returns the current lifecycle state.
func (m *turnMachine) State() TurnState { return m.state }

// To advances to next when the transition is legal, returning true. Illegal
// transitions — including any move out of a terminal state or a no-op re-entry
// of the current state — are ignored and return false.
func (m *turnMachine) To(next TurnState) bool {
	if !m.state.CanTransitionTo(next) {
		return false
	}
	m.state = next
	return true
}

// Terminal returns the turn's terminal state, or false if it is still running.
func (m *turnMachine) Terminal() (TurnState, bool) {
	if m.state.IsTerminal() {
		return m.state, true
	}
	return TurnIdle, false
}
