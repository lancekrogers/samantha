package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// speechFor runs one turn and returns everything TTS was asked to say, joined
// the way a listener would hear it. runner picks the path under test.
func speechFor(t *testing.T, chunks []string, textMode bool) string {
	t.Helper()
	bus := events.NewBus()
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: chunks},
		TTS:    ttsProvider,
		Player: player,
		Events: bus,
	}
	if textMode {
		if err := p.RunTurnTextMode(context.Background(), "hello"); err != nil {
			t.Fatalf("RunTurnTextMode() error = %v", err)
		}
	} else if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	return strings.Join(ttsProvider.Texts(), " ")
}

// TestTextModeSpeaksTheSameAsProgressive is the point of speakableFromResponse.
// A typed turn and a spoken turn are the same conversation, so they must not
// say different things — before this, text mode synthesized the gated reply
// verbatim and spoke markdown and fillers that voice mode had already dropped.
func TestTextModeSpeaksTheSameAsProgressive(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
	}{
		{"filler only, replaced by recovery", []string{"Hmm.", " Hmm, I lost my train of thought for a second. What were you saying?"}},
		{"markdown and a stripped filler", []string{"Here is **bold** text. Umm, and a second thought."}},
		{"plain reply", []string{"The capital of France is Paris. It has about two million people."}},
		{"voiced filler leading real content", []string{"Hmm, that is a good question. The answer is yes."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			progressive := speechFor(t, tc.chunks, false)
			typed := speechFor(t, tc.chunks, true)
			if progressive != typed {
				t.Errorf("paths disagree:\n  progressive: %q\n  text mode:   %q", progressive, typed)
			}
		})
	}
}

// TestTextModeDropsFillerOnlyAndMarkdown pins the two concrete regressions,
// so a parity break cannot be "fixed" by making both paths wrong together.
func TestTextModeDropsFillerOnlyAndMarkdown(t *testing.T) {
	t.Run("filler is not spoken before the recovery line", func(t *testing.T) {
		got := speechFor(t, []string{"Hmm.", " Hmm, I lost my train of thought for a second. What were you saying?"}, true)
		if strings.HasPrefix(got, "Hmm.") {
			t.Errorf("text mode still speaks the bare hesitation: %q", got)
		}
		if want := "Hmm, I lost my train of thought for a second. What were you saying?"; got != want {
			t.Errorf("spoke %q, want %q", got, want)
		}
	})

	t.Run("markdown does not reach TTS", func(t *testing.T) {
		got := speechFor(t, []string{"Here is **bold** text. Umm, and a second thought."}, true)
		if strings.Contains(got, "**") {
			t.Errorf("text mode still speaks markdown: %q", got)
		}
	})
}

// TestSpeakableFromResponseNeedsSegmentGranularity guards the shortcut a future
// reader will reach for: calling speakableSegment on the whole reply instead of
// per segment. That looks equivalent and is not — the AND passes as soon as any
// real content is present, so the filler rides along on the same string.
func TestSpeakableFromResponseNeedsSegmentGranularity(t *testing.T) {
	const glued = "Hmm. Hmm, I lost my train of thought for a second. What were you saying?"

	if !speakableSegment(glued) {
		t.Fatal("precondition failed: the whole-reply predicate is expected to accept this string")
	}
	got := speakableFromResponse(glued)
	if got == glued {
		t.Errorf("speakableFromResponse returned the reply unchanged — segment filtering is not happening: %q", got)
	}
	if strings.HasPrefix(got, "Hmm.") {
		t.Errorf("leading filler survived segmentation: %q", got)
	}
}

func TestSpeakableFromResponseEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", ".", "Hmm.", "\u200b"} {
		if got := speakableFromResponse(in); got != "" {
			t.Errorf("speakableFromResponse(%q) = %q, want empty", in, got)
		}
	}
}
