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

func TestPrepareKokoroTextExpandsNTContractions(t *testing.T) {
	// Hyphenating (was-n't) avoided U+0329 skips but sounded broken in
	// audiobooks. Expansion keeps natural narration without skips.
	in := "It wasn't, isn't, and shouldn't be noisy. Won't and can't expand too. Don't wait."
	want := "It was not, is not, and should not be noisy. Will not and cannot expand too. Do not wait."

	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextExpandsCurlyApostropheContractions(t *testing.T) {
	in := "She wasn\u2019t ready and he wouldn\u2019t go."
	want := "She was not ready and he would not go."
	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextContractionCase(t *testing.T) {
	cases := map[string]string{
		"Wasn't": "Was not",
		"WASN'T": "WAS NOT",
		"wasn't": "was not",
		"Don't":  "Do not",
		"CAN'T":  "CANNOT",
	}
	for in, want := range cases {
		if got := PrepareKokoroText(in); got != want {
			t.Errorf("PrepareKokoroText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepareKokoroTextUnknownNTFallsBackToHyphen(t *testing.T) {
	// Not in the expansion table — keep the legacy U+0329 dodge.
	in := "They daren't try."
	want := "They dare-n't try."
	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
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
