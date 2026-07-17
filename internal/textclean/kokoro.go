package textclean

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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

// On thewh1teagle Kokoro v1.0 + sherpa, most *n't contractions synthesize cleanly
// and should stay contracted (more human). "wasn't" is the outlier: eSpeak emits
// U+0329 and sherpa drops the phone. Stripping the apostrophe (wasn't → wasnt)
// keeps a contracted shape without the skip or a full "was not" expansion.
var kokoroWasnt = regexp.MustCompile(`(?i)\bwasn['’]t\b`)

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
// vocabulary. The model path can phonemize some ordinary words with U+0329
// (syllabic n), which Kokoro does not have a token for. Sherpa then skips those
// phonemes — dropping segments of speech.
//
// Syllable boundaries steer eSpeak for known stems (button → but-ton). Most
// English *n't contractions are left alone so narration stays human; only
// "wasn't" is normalized (→ wasnt) after measured U+0329 skips on v1.0.
//
// This is a synthesis-boundary transformation. Conversation text / UI should
// keep the user's original spelling.
func PrepareKokoroText(s string) string {
	s = StripUnsupportedKokoroMarks(s)
	s = kokoroSyllabicNWords.ReplaceAllStringFunc(s, rewriteSyllabicNWord)
	return kokoroWasnt.ReplaceAllStringFunc(s, rewriteWasnt)
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

func rewriteWasnt(word string) string {
	// wasn't / Wasn’t / WASN'T → wasnt / Wasnt / WASNT
	return matchLetterCase(word, "wasnt")
}

// matchLetterCase copies capitalization style from src onto a lowercase ASCII
// replacement (title → first upper, all-caps → all caps, else lower).
func matchLetterCase(src, lowerASCII string) string {
	if src == "" || lowerASCII == "" {
		return lowerASCII
	}
	letters, allUpper := 0, true
	for _, r := range src {
		if unicode.IsLetter(r) {
			letters++
			if !unicode.IsUpper(r) {
				allUpper = false
			}
		}
	}
	if letters > 1 && allUpper {
		return strings.ToUpper(lowerASCII)
	}
	first, _ := utf8.DecodeRuneInString(src)
	if unicode.IsUpper(first) {
		rf, size := utf8.DecodeRuneInString(lowerASCII)
		return string(unicode.ToUpper(rf)) + lowerASCII[size:]
	}
	return lowerASCII
}
