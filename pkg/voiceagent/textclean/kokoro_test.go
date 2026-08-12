package textclean

import (
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

func TestPrepareKokoroTextPreservesContractionsAndStems(t *testing.T) {
	// Real human forms must survive prep. Syllabic-n is fixed in tokens, not
	// by hyphenating (was-n't / but-ton) or expanding (was not).
	in := "It wasn't, isn't, and shouldn't be noisy. Won't and can't stay. Forgotten buttons get written carefully."
	if got := PrepareKokoroText(in); got != in {
		t.Fatalf("PrepareKokoroText() rewrote natural text:\n got %q\nwant %q", got, in)
	}
}

func TestPrepareKokoroTextStripsMarksFromInput(t *testing.T) {
	in := "cafe\u0301 and written"
	want := "cafe and written"

	if got := PrepareKokoroText(in); got != want {
		t.Fatalf("PrepareKokoroText() = %q, want %q", got, want)
	}
}

func TestPrepareKokoroTextGoldenABPhrase(t *testing.T) {
	// Same campaign phrase; prep no longer hyphenates stems (tokens alias).
	const in = "Hello, I'm Samantha. Forgotten buttons get written down carefully."
	if got := PrepareKokoroText(in); got != in {
		t.Fatalf("PrepareKokoroText() = %q, want unchanged %q", got, in)
	}
}
