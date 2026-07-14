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

// Stems that eSpeak phonemizes with syllabic-n (U+0329), which Kokoro's token
// set rejects — sherpa then *skips* the phoneme and the spoken word is wrong
// or truncated. Regular forms and regular plurals (button/buttons) are matched.
var kokoroSyllabicNWords = regexp.MustCompile(`(?i)\b(?:forgotten|written|buttons?|kittens?|cottons?|certain|bitten|gotten)\b`)

// n't contractions where eSpeak also emits syllabic-n (wasn't, isn't, …).
// Won't / can't stay unchanged (different phonology).
var kokoroSyllabicNContractions = regexp.MustCompile(`(?i)\b(?:ain|are|could|did|does|had|has|is|must|need|should|was|were|would)n['’]t\b`)

// Stem (lowercase) → hyphenated form that steers eSpeak away from U+0329.
// Plurals append "s" to the hyphenated stem (button → but-ton → but-tons).
var kokoroSyllabicNStems = map[string]string{
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
// phonemizes some ordinary words with U+0329 (syllabic n), which Kokoro does
// not have a token for. Sherpa then skips those phonemes — so the Go path can
// drop segments of speech and produce shorter, wrong-sounding audio versus
// Python kokoro-onnx (which uses a different frontend and assets).
//
// Syllable boundaries steer eSpeak to a safer phone sequence. Regular plurals
// of known stems (e.g. buttons) are rewritten too; leaving them unchanged was
// a confirmed A/B gap (golden phrase still logged U+0329 skips).
//
// This is a synthesis-boundary transformation. Conversation text / UI should
// keep the user's original spelling.
func PrepareKokoroText(s string) string {
	s = StripUnsupportedKokoroMarks(s)
	s = kokoroSyllabicNWords.ReplaceAllStringFunc(s, rewriteSyllabicNWord)
	return kokoroSyllabicNContractions.ReplaceAllStringFunc(s, rewriteSyllabicNContraction)
}

func rewriteSyllabicNWord(word string) string {
	lower := strings.ToLower(word)
	if rep, ok := kokoroSyllabicNStems[lower]; ok {
		return rep
	}
	// Regular plural: buttons → but-tons from stem button.
	if strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "ss") {
		stem := lower[:len(lower)-1]
		if rep, ok := kokoroSyllabicNStems[stem]; ok {
			return rep + "s"
		}
	}
	return word
}

func rewriteSyllabicNContraction(word string) string {
	// Insert a hyphen before the final n of …n't so eSpeak does not emit U+0329.
	for i := len(word) - 1; i >= 0; i-- {
		if word[i] == 'n' || word[i] == 'N' {
			return word[:i] + "-" + word[i:]
		}
	}
	return word
}
