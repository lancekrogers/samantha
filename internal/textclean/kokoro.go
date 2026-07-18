package textclean

import (
	"strings"
)

var unsupportedKokoroMarks = strings.NewReplacer(
	"\u0329", "", // combining vertical line below (rare in *input* text)
	"\u0300", "", // combining grave accent
	"\u0301", "", // combining acute accent
	"\u0327", "", // combining cedilla
)

// StripUnsupportedKokoroMarks removes combining marks that sherpa-onnx Kokoro
// currently emits as unknown phonemes when they appear in input text.
func StripUnsupportedKokoroMarks(s string) string {
	return unsupportedKokoroMarks.Replace(s)
}

// PrepareKokoroText is the synthesis-boundary text transform for Kokoro.
//
// Contractions and ordinary English are left alone so narration stays human
// ("wasn't", "isn't", "don't"). eSpeak may still emit syllabic-n (U+0329) for
// some of those words; that is handled by aliasing U+0329 in the Kokoro tokens
// file (see tts.ensureKokoroTokensWithSyllabicN), not by rewriting the text.
//
// Input cleanup still strips a few combining marks if they appear in source
// text (PDF artifacts, etc.). Conversation UI should keep original spelling.
func PrepareKokoroText(s string) string {
	return StripUnsupportedKokoroMarks(s)
}
