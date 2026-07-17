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

// *n't contractions (straight or curly apostrophe). eSpeak often emits syllabic-n
// for these; hyphenating (was-n't) avoids the skip but sounds broken in
// audiobooks. Prefer expanding to full forms when we know the mapping.
var kokoroNTContraction = regexp.MustCompile(`(?i)\b[a-zA-Z]+n['’]t\b`)

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

// Lowercase *n't form → expanded form for synthesis. Expansion avoids both
// U+0329 skips and the awkward was-n't / is-n't hyphenation that made
// audiobook narration sound glitchy.
var kokoroNTExpansions = map[string]string{
	"ain't":     "is not",
	"aren't":    "are not",
	"can't":     "cannot",
	"couldn't":  "could not",
	"didn't":    "did not",
	"doesn't":   "does not",
	"don't":     "do not",
	"hadn't":    "had not",
	"hasn't":    "has not",
	"haven't":   "have not",
	"isn't":     "is not",
	"mightn't":  "might not",
	"mustn't":   "must not",
	"needn't":   "need not",
	"oughtn't":  "ought not",
	"shan't":    "shall not",
	"shouldn't": "should not",
	"wasn't":    "was not",
	"weren't":   "were not",
	"won't":     "will not",
	"wouldn't":  "would not",
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
// Syllable boundaries steer eSpeak to a safer phone sequence for known stems.
// *n't contractions are expanded to full forms (wasn't → was not) so audiobook
// narration stays natural; hyphenating (was-n't) avoided skips but sounded broken.
//
// This is a synthesis-boundary transformation. Conversation text / UI should
// keep the user's original spelling.
func PrepareKokoroText(s string) string {
	s = StripUnsupportedKokoroMarks(s)
	s = kokoroSyllabicNWords.ReplaceAllStringFunc(s, rewriteSyllabicNWord)
	return kokoroNTContraction.ReplaceAllStringFunc(s, rewriteNTContraction)
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

func rewriteNTContraction(word string) string {
	key := normalizeApostrophe(strings.ToLower(word))
	if exp, ok := kokoroNTExpansions[key]; ok {
		return matchWordCase(word, exp)
	}
	// Unknown *n't form: fall back to hyphen before final n (legacy U+0329 dodge).
	return hyphenateBeforeFinalN(word)
}

func normalizeApostrophe(s string) string {
	s = strings.ReplaceAll(s, "\u2019", "'") // right single quotation
	s = strings.ReplaceAll(s, "\u2018", "'") // left single quotation
	s = strings.ReplaceAll(s, "\u02BC", "'") // modifier letter apostrophe
	return s
}

func hyphenateBeforeFinalN(word string) string {
	for i := len(word) - 1; i >= 0; i-- {
		if word[i] == 'n' || word[i] == 'N' {
			return word[:i] + "-" + word[i:]
		}
	}
	return word
}

// matchWordCase applies the source token's capitalization style to the
// expanded form (Wasn't → Was not, WASN'T → WAS NOT, wasn't → was not).
func matchWordCase(src, exp string) string {
	if src == "" || exp == "" {
		return exp
	}
	// All-caps token (at least two letters): expand in all caps.
	letters := 0
	allUpper := true
	for _, r := range src {
		if unicode.IsLetter(r) {
			letters++
			if !unicode.IsUpper(r) {
				allUpper = false
			}
		}
	}
	if letters > 1 && allUpper {
		return strings.ToUpper(exp)
	}
	// Leading capital → capitalize first letter of expansion.
	first, size := utf8.DecodeRuneInString(src)
	if unicode.IsUpper(first) {
		ef, es := utf8.DecodeRuneInString(exp)
		if ef == utf8.RuneError {
			return exp
		}
		return string(unicode.ToUpper(ef)) + exp[es:]
	}
	_ = size
	return exp
}
