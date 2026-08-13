package pipeline

import (
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
)

// TestSpeakableSegmentNeedsBothHalves is a guard, not a behaviour test. It
// fails if speakableSegment is ever "simplified" to one of its two predicates.
//
// The trap is that each half looks like it subsumes the other, and neither
// does. PR #223's review found the consequence: a path holding only the weaker
// predicate spoke a bare "Hmm." while the recovery line went to history alone.
func TestSpeakableSegmentNeedsBothHalves(t *testing.T) {
	t.Run("HasSpeakableContent alone would let a bare hesitation through", func(t *testing.T) {
		for _, filler := range []string{"Hmm.", "hmm", "Haha!", "hmm hmm"} {
			if !hasPronounceableContent(filler) {
				t.Errorf("%q: expected the pronounceable half to accept a voiced filler", filler)
			}
			if brain.HasSpeakableContent(filler) {
				t.Errorf("%q: expected the speakable half to reject a voiced filler", filler)
			}
			if speakableSegment(filler) {
				t.Errorf("%q: a filler-only segment must not be spoken or displayed", filler)
			}
		}
	})

	t.Run("hasPronounceableContent alone would let invisible runes through", func(t *testing.T) {
		// A zero-width space and a lone combining acute, written as escapes:
		// literal invisible runes in source do not survive editors reliably.
		// brain's predicate trims spaces, punctuation and symbols; a format or
		// mark rune is none of those, so it survives the trim as "content".
		for _, invisible := range []string{"\u200b", "\u0301", "\u200b\u200b", "\ufeff"} {
			if !brain.HasSpeakableContent(invisible) {
				t.Errorf("%q: expected the speakable half to accept an invisible rune", invisible)
			}
			if hasPronounceableContent(invisible) {
				t.Errorf("%q: expected the pronounceable half to reject an invisible rune", invisible)
			}
			if speakableSegment(invisible) {
				t.Errorf("%q: an invisible-only segment must not reach TTS", invisible)
			}
		}
	})

	t.Run("real content survives both", func(t *testing.T) {
		for _, text := range []string{"Hello.", "42.", "$5", "R2D2", "12:00", "Hmm, yes.", "日本語"} {
			if !speakableSegment(text) {
				t.Errorf("%q: real content must be spoken", text)
			}
		}
	})

	t.Run("empty and punctuation-only are rejected", func(t *testing.T) {
		for _, junk := range []string{"", ".", "...", "  ", "?!", "—", "\t\n"} {
			if speakableSegment(junk) {
				t.Errorf("%q: must not reach TTS", junk)
			}
		}
	})
}
