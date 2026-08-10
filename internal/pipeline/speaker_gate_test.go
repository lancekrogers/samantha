package pipeline

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

type countingBrain struct {
	fakeBrain
	streams atomic.Int64
}

func (c *countingBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	c.streams.Add(1)
	return c.fakeBrain.ThinkStream(ctx, input, opts)
}

func TestRunTurnSpeakerGateVetoesTurn(t *testing.T) {
	brainProvider := &countingBrain{fakeBrain: fakeBrain{chunks: []string{"must never run"}}}
	verified := false
	p := &Pipeline{
		STT:            &fakeSTT{text: "what is the wifi password"},
		Brain:          brainProvider,
		VAD:            &fakeVAD{},
		Capture:        newFakeCapture(),
		Events:         events.NewBus(),
		CurrentSpeaker: func() string { return "speaker-2" },
		SpeakerGate: func(label string) string {
			if !verified {
				return "utterance ignored: unverified speaker " + label
			}
			return ""
		},
	}

	// Vetoed: quiet no-op turn, brain untouched.
	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text != "" {
		t.Fatalf("vetoed turn returned text %q", text)
	}
	if n := brainProvider.streams.Load(); n != 0 {
		t.Fatalf("brain called %d time(s) on a vetoed turn", n)
	}

	// The enrolled owner speaks: the turn proceeds normally.
	verified = true
	text, err = p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text == "" {
		t.Fatal("allowed turn returned no text")
	}
	if n := brainProvider.streams.Load(); n != 1 {
		t.Fatalf("brain calls = %d, want 1", n)
	}
}
