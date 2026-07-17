package textclean

import (
	"strings"
	"testing"
)

func TestStripUnsupportedKokoroMarks(t *testing.T) {
	in := "syllabic n\u0329, cafe\u0301, facade\u0327, à la carte"
	got := StripUnsupportedKokoroMarks(in)
	want := "syllabic n, cafe, facade, a la carte"

	if got != want {
		t.Fatalf("StripUnsupportedKokoroMarks() = %q, want %q", got, want)
	}
}

func TestStripUnsupportedKokoroMarksPreservesPrecomposedLetters(t *testing.T) {
	in := "cafe café façade"
	got := StripUnsupportedKokoroMarks(in)

	if got != in {
		t.Fatalf("StripUnsupportedKokoroMarks() = %q, want %q", got, in)
	}
}

func TestPrepareKokoroTextAvoidsUnsupportedSyllabicN(t *testing.T) {
	in := "Written, button, kitten, cotton, certain, bitten, gotten, forgotten. Unwritten stays."
	want := "writ-ten, but-ton, kit-ten, cot-ton, cer-tain, bit-ten, got-ten, for-got-ten. Unwritten stays."

	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextRewritesRegularPlurals(t *testing.T) {
	// Campaign A/B phrase used "buttons" (plural); stem-only matching left it
	// unchanged and sherpa still skipped U+0329 phonemes.
	in := "Forgotten buttons and kittens."
	want := "for-got-ten but-tons and kit-tens."
	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextAlsoStripsMarksFromInput(t *testing.T) {
	in := "cafe\u0301 and written"
	want := "cafe and writ-ten"

	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextKeepsHumanContractions(t *testing.T) {
	// Measured on thewh1teagle v1.0: only "wasn't" emits U+0329. Other *n't
	// forms should stay contracted so narration sounds human — not expanded
	// to "is not" / "will not" and not hyphenated to "is-n't".
	in := "It isn't, shouldn't, wouldn't, don't, can't, and won't change. Weren't we ready?"
	if got := PrepareKokoroText(in); got != in {
		t.Fatalf("PrepareKokoroText() rewrote healthy contractions:\n got %q\nwant %q", got, in)
	}
}

func TestPrepareKokoroTextNormalizesWasntOnly(t *testing.T) {
	// wasn't → wasnt avoids the sole *n't U+0329 skip without sounding like
	// "was not" or the broken "was-n't" hyphenation.
	cases := map[string]string{
		"It wasn't ready.":          "It wasnt ready.",
		"Wasn't it?":                "Wasnt it?",
		"WASN'T":                    "WASNT",
		"She wasn\u2019t there.":    "She wasnt there.",
		"They aren't and wasn't.":   "They aren't and wasnt.",
	}
	for in, want := range cases {
		if got := PrepareKokoroText(in); got != want {
			t.Errorf("PrepareKokoroText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Golden A/B phrase from scripts/voice-agent-test — documents the exact
// synthesis-boundary transform that made Go WAV content diverge from Python.
func TestPrepareKokoroTextGoldenABPhrase(t *testing.T) {
	const in = "Hello, I'm Samantha. Forgotten buttons get written down carefully."
	got := PrepareKokoroText(in)
	const want = "Hello, I'm Samantha. for-got-ten but-tons get writ-ten down carefully."
	if got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
	// Guard: never leave bare plurals of known stems (they trigger U+0329 skips).
	for _, bare := range []string{"buttons", "Buttons", "forgotten", "Forgotten", "written", "Written"} {
		// "forgotten"/"written" appear only inside hyphenated forms after prep.
		if bare == "forgotten" || bare == "Forgotten" || bare == "written" || bare == "Written" {
			if strings.Contains(got, bare) && !strings.Contains(got, "for-got-ten") && !strings.Contains(got, "writ-ten") {
				t.Fatalf("prepared text still contains unfixed %q: %q", bare, got)
			}
			continue
		}
		if strings.Contains(got, bare) {
			t.Fatalf("prepared text still contains bare %q: %q", bare, got)
		}
	}
}
