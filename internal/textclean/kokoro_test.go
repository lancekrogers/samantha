package textclean

import "testing"

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
