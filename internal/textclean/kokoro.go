package textclean

import (
	"regexp"
	"strings"
)

var unsupportedKokoroMarks = strings.NewReplacer(
	"\u0329", "", // combining vertical line below
	"\u0300", "", // combining grave accent
	"\u0301", "", // combining acute accent
	"\u0327", "", // combining cedilla
)

var kokoroSyllabicNWords = regexp.MustCompile(`(?i)\b(?:forgotten|written|button|kitten|cotton|certain|bitten|gotten)\b`)

var kokoroSyllabicNPronunciations = map[string]string{
	"forgotten": "for-got-ten",
	"written":   "writ-ten",
	"button":    "but-ton",
	"kitten":    "kit-ten",
	"cotton":    "cot-ton",
	"certain":   "cer-tain",
	"bitten":    "bit-ten",
	"gotten":    "got-ten",
}

// StripUnsupportedKokoroMarks removes combining marks that sherpa-onnx Kokoro
// currently emits as unknown phonemes.
func StripUnsupportedKokoroMarks(s string) string {
	return unsupportedKokoroMarks.Replace(s)
}

// PrepareKokoroText keeps sherpa-onnx's eSpeak frontend inside Kokoro's token
// vocabulary. The multilingual model ignores its supplied English lexicon and
// phonemizes these ordinary words with U+0329 (syllabic n), which Kokoro does
// not have a token for. Syllable boundaries preserve the spoken word while
// avoiding the dropped phoneme and native stderr warning.
//
// This is a synthesis-boundary transformation. Conversation text should keep
// the user's original spelling.
func PrepareKokoroText(s string) string {
	s = StripUnsupportedKokoroMarks(s)
	return kokoroSyllabicNWords.ReplaceAllStringFunc(s, func(word string) string {
		return kokoroSyllabicNPronunciations[strings.ToLower(word)]
	})
}
