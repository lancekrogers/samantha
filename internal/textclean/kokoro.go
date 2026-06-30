package textclean

import "strings"

var unsupportedKokoroMarks = strings.NewReplacer(
	"\u0329", "", // combining vertical line below
	"\u0300", "", // combining grave accent
	"\u0301", "", // combining acute accent
	"\u0327", "", // combining cedilla
)

// StripUnsupportedKokoroMarks removes combining marks that sherpa-onnx Kokoro
// currently emits as unknown phonemes.
func StripUnsupportedKokoroMarks(s string) string {
	return unsupportedKokoroMarks.Replace(s)
}
