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

func TestPrepareKokoroTextAvoidsUnsupportedSyllabicN(t *testing.T) {
	in := "Written, button, kitten, cotton, certain, bitten, gotten, forgotten. Unwritten stays."
	want := "writ-ten, but-ton, kit-ten, cot-ton, cer-tain, bit-ten, got-ten, for-got-ten. Unwritten stays."

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
