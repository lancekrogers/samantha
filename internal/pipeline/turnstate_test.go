package pipeline

import (
	"testing"
)

func TestTurnStateString(t *testing.T) {
	cases := map[TurnState]string{
		TurnIdle:         "idle",
		TurnListening:    "listening",
		TurnTranscribing: "transcribing",
		TurnThinking:     "thinking",
		TurnSpeaking:     "speaking",
		TurnInterrupted:  "interrupted",
		TurnCompleted:    "completed",
		TurnFailed:       "failed",
		TurnTimedOut:     "timed_out",
		TurnState(99):    "unknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("TurnState(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}

func TestTurnStateIsTerminal(t *testing.T) {
	terminal := []TurnState{TurnInterrupted, TurnCompleted, TurnFailed, TurnTimedOut}
	running := []TurnState{TurnIdle, TurnListening, TurnTranscribing, TurnThinking, TurnSpeaking}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%s.IsTerminal() = false, want true", s)
		}
	}
	for _, s := range running {
		if s.IsTerminal() {
			t.Errorf("%s.IsTerminal() = true, want false", s)
		}
	}
}

// drive a sequence of transitions through a fresh machine, asserting each step
// is legal, and return the machine for terminal inspection.
func driveStates(t *testing.T, steps ...TurnState) *turnMachine {
	t.Helper()
	m := newTurnMachine()
	for i, step := range steps {
		if !m.To(step) {
			t.Fatalf("step %d: To(%s) from %s rejected, want legal", i, step, m.State())
		}
		if m.State() != step {
			t.Fatalf("step %d: state = %s, want %s", i, m.State(), step)
		}
	}
	return m
}

func TestTurnMachineNormalCompletion(t *testing.T) {
	m := driveStates(t, TurnListening, TurnTranscribing, TurnThinking, TurnSpeaking, TurnCompleted)
	got, ok := m.Terminal()
	if !ok || got != TurnCompleted {
		t.Fatalf("Terminal() = (%s, %v), want (completed, true)", got, ok)
	}
}

func TestTurnMachineTextModeCompletion(t *testing.T) {
	// Text mode enters straight at thinking (no microphone) and may complete
	// without speaking when TTS is unavailable.
	m := driveStates(t, TurnThinking, TurnCompleted)
	if got, ok := m.Terminal(); !ok || got != TurnCompleted {
		t.Fatalf("Terminal() = (%s, %v), want (completed, true)", got, ok)
	}
}

func TestTurnMachineNoSpeech(t *testing.T) {
	// No speech / silence: listening ends without a transcript.
	m := driveStates(t, TurnListening, TurnTimedOut)
	if got, ok := m.Terminal(); !ok || got != TurnTimedOut {
		t.Fatalf("Terminal() = (%s, %v), want (timed_out, true)", got, ok)
	}

	// Empty transcript after transcription also times out.
	m = driveStates(t, TurnListening, TurnTranscribing, TurnTimedOut)
	if got, _ := m.Terminal(); got != TurnTimedOut {
		t.Fatalf("Terminal() = %s, want timed_out", got)
	}
}

func TestTurnMachineProviderFailure(t *testing.T) {
	// STT provider error while listening.
	m := driveStates(t, TurnListening, TurnFailed)
	if got, ok := m.Terminal(); !ok || got != TurnFailed {
		t.Fatalf("stt failure Terminal() = (%s, %v), want (failed, true)", got, ok)
	}

	// Brain provider error while thinking.
	m = driveStates(t, TurnListening, TurnThinking, TurnFailed)
	if got, _ := m.Terminal(); got != TurnFailed {
		t.Fatalf("brain failure Terminal() = %s, want failed", got)
	}

	// Playback/TTS error while speaking.
	m = driveStates(t, TurnListening, TurnThinking, TurnSpeaking, TurnFailed)
	if got, _ := m.Terminal(); got != TurnFailed {
		t.Fatalf("playback failure Terminal() = %s, want failed", got)
	}
}

func TestTurnMachineCancellation(t *testing.T) {
	// Cancellation can land in any pre-terminal stage; each routes to interrupted.
	for _, stage := range [][]TurnState{
		{TurnListening, TurnInterrupted},
		{TurnListening, TurnTranscribing, TurnInterrupted},
		{TurnListening, TurnThinking, TurnInterrupted},
	} {
		m := driveStates(t, stage...)
		if got, ok := m.Terminal(); !ok || got != TurnInterrupted {
			t.Fatalf("cancel from %v Terminal() = (%s, %v), want (interrupted, true)", stage, got, ok)
		}
	}
}

func TestTurnMachineInterruption(t *testing.T) {
	// Barge-in during playback.
	m := driveStates(t, TurnListening, TurnThinking, TurnSpeaking, TurnInterrupted)
	if got, ok := m.Terminal(); !ok || got != TurnInterrupted {
		t.Fatalf("Terminal() = (%s, %v), want (interrupted, true)", got, ok)
	}
}

func TestTurnMachineRejectsInvalidTransitions(t *testing.T) {
	// Cannot skip straight from idle to speaking.
	m := newTurnMachine()
	if m.To(TurnSpeaking) {
		t.Error("To(speaking) from idle accepted, want rejected")
	}
	if m.State() != TurnIdle {
		t.Errorf("state mutated to %s on rejected transition, want idle", m.State())
	}

	// Re-entering the current state is a no-op, not a transition.
	driveStates(t, TurnListening)
	m = newTurnMachine()
	m.To(TurnListening)
	if m.To(TurnListening) {
		t.Error("To(listening) from listening accepted, want rejected (no self-loop)")
	}

	// Terminal states are absorbing.
	m = driveStates(t, TurnListening, TurnTimedOut)
	for _, next := range []TurnState{TurnListening, TurnThinking, TurnSpeaking, TurnCompleted, TurnFailed} {
		if m.To(next) {
			t.Errorf("To(%s) from terminal timed_out accepted, want rejected", next)
		}
		if m.State() != TurnTimedOut {
			t.Fatalf("terminal state mutated to %s, want timed_out", m.State())
		}
	}
}
