package textclean

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PrepareSpokenText is the transform applied to text on its way into a speech
// synthesiser, for every provider.
//
// It is deliberately small. The rule this package follows is **expand only from
// observed misrenders**, and the measurements behind each rule are recorded in
// docs/audio/golden/N6/provenance.json:
//
//   - Emoji are stripped. Kokoro spoke them aloud — "Done 🎉" ran 0.617 s longer
//     than "Done" because the emoji name was being read out.
//   - Four-digit years are read as years. "1999" reached eSpeak as "one thousand
//     nine hundred ninety-nine"; as a year it is 0.285 s shorter and is how a
//     person says it.
//
// What is deliberately NOT here: plain integers and simple decimals. Measuring
// showed eSpeak already renders them as words — "42" and "forty-two" differ by
// 0.010 s, "3.14 times 13" by 0.056 s. Expanding them again bought nothing and
// cost a great deal, because a number touching a non-space character is where
// naive expansion breaks: "555-1234", "2026-08-11", "$5", "R2D2" and "12:00" all
// garbled. See decision D008.
func PrepareSpokenText(s string) string {
	return expandYears(StripEmoji(s))
}

// --- emoji -----------------------------------------------------------------

// emojiRanges covers the blocks a chat model actually reaches for.
//
// It is deliberately not the full Unicode emoji property. Ranges whose contents
// carry meaning when spoken — currency, typographic dashes and quotes, the
// ellipsis, letterlike symbols such as № and ℅ — are left out so stripping never
// eats real prose.
//
// It is not free of collateral, and pretending otherwise would be worse than the
// collateral itself: U+2600-27BF contains check marks (✓) and card suits along
// with the pictographs, and those go too. The characters in emojiKeep are the
// exceptions where losing the glyph would corrupt the sentence's meaning rather
// than just remove decoration.
var emojiRanges = [...]struct{ lo, hi rune }{
	{0x1F000, 0x1FAFF}, // mahjong, cards, pictographs, emoticons, transport, extended-A
	{0x2600, 0x27BF},   // miscellaneous symbols and dingbats
	{0x2B00, 0x2BFF},   // miscellaneous symbols and arrows
	{0x231A, 0x231B},   // ⌚ ⌛ — emoji by default, outside the blocks above
	{0x23E9, 0x23FA},   // ⏩ ⏰ ⏳ ⏸ ⏺ — media and clock controls
	{0x25AA, 0x25FE},   // geometric shapes used as emoji (▪ ▶ ◾)
	{0xFE00, 0xFE0F},   // variation selectors
	{0xE0020, 0xE007F}, // tag characters, the invisible tail of flag sequences
}

// emojiSingles are joiners, modifiers and lone pictographs outside the ranges.
// Leaving a joiner behind turns a stripped emoji into a stray unpronounceable
// rune that no whitespace cleanup can reach, because it is not whitespace.
var emojiSingles = map[rune]bool{
	0x200D: true, // zero-width joiner
	0x20E3: true, // combining enclosing keycap
	0x203C: true, // ‼ double exclamation
	0x2049: true, // ⁉ exclamation question
	0x3030: true, // wavy dash
	0x303D: true, // part alternation mark
	0x3297: true, // circled ideograph congratulation
	0x3299: true, // circled ideograph secret
}

// emojiKeep are characters inside the ranges above that must survive, because
// deleting them changes what the sentence says rather than how it is decorated.
// "3 ✕ 4" without the operator is "three four" — two operands run together as if
// they were a list.
var emojiKeep = map[rune]bool{
	0x2715: true, // ✕ multiplication
	0x2716: true, // ✖ heavy multiplication
	0x266D: true, // ♭ flat
	0x266E: true, // ♮ natural
	0x266F: true, // ♯ sharp
}

const variationSelector16 = 0xFE0F

// isKeycapBase reports whether a character can carry a combining enclosing
// keycap — the digits, '#' and '*'.
func isKeycapBase(r rune) bool {
	return (r >= '0' && r <= '9') || r == '#' || r == '*'
}

func isEmojiRune(r rune) bool {
	if emojiKeep[r] {
		return false
	}
	if emojiSingles[r] {
		return true
	}
	for _, rg := range emojiRanges {
		if r >= rg.lo && r <= rg.hi {
			return true
		}
	}
	return false
}

// StripEmoji removes emoji unconditionally, and repairs the spacing it damages.
//
// The persona document already tells the model not to use them; this is what
// happens when it does anyway, which is the failure mode that actually reaches a
// listener's ears.
//
// A base character followed by U+FE0F is removed too. That selector is what turns
// an ordinary symbol into an emoji ("▶" into "▶️"), so its presence is the
// text's own statement that the character is decoration. Handling it this way
// covers ▶️ ⏸️ ℹ️ ↩️ Ⓜ️ without enumerating every block they live in, and leaves
// the same characters alone when they appear bare and probably mean something.
func StripEmoji(s string) string {
	if !strings.ContainsFunc(s, isEmojiRune) {
		return s
	}

	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		if isEmojiRune(rs[i]) {
			continue
		}
		// A base character wearing an emoji presentation selector. The selector
		// itself is dropped by the check above on the next iteration.
		//
		// Keycap bases are the exception: "1️⃣" is a digit in costume, and the
		// digit is usually doing real work — enumerating a list. Drop the
		// costume, keep the digit.
		if i+1 < len(rs) && rs[i+1] == variationSelector16 && !isKeycapBase(rs[i]) {
			continue
		}
		b.WriteRune(rs[i])
	}
	return repairSpacing(b.String())
}

var (
	repeatedSpaceRE   = regexp.MustCompile(`[ \t]{2,}`)
	spaceBeforePunc   = regexp.MustCompile(`[ \t]+([,!?;:])`)
	spaceBeforePeriod = regexp.MustCompile(`[ \t]+\.($|[^0-9])`)
)

// repairSpacing closes the gaps left where an emoji used to be, so "Nice 👍 work"
// does not reach the synthesiser as "Nice  work" and "Done! 🎉" does not become
// "Done! ".
//
// It runs ONLY on text that lost an emoji. Applied unconditionally it would
// rewrite ordinary prose it has no business touching — "5 . 5" would become
// "5. 5", manufacturing a sentence boundary out of stray whitespace.
//
// A period is only sentence punctuation when a digit does not follow it;
// otherwise " .5" would collapse into ".5" and swallow the space before a
// decimal.
func repairSpacing(s string) string {
	s = repeatedSpaceRE.ReplaceAllString(s, " ")
	s = spaceBeforePunc.ReplaceAllString(s, "$1")
	s = spaceBeforePeriod.ReplaceAllString(s, ".$1")
	return strings.TrimSpace(s)
}

// --- years -----------------------------------------------------------------

// Four-digit numbers in this range are read as years — "nineteen ninety-nine"
// rather than "one thousand nine hundred ninety-nine" — because in a spoken reply
// from this agent that is what they almost always are.
//
// The known collision is technical constants inside the range: 1440 reads as
// "fourteen forty". That is how a person says it out loud, so the range was not
// narrowed around it. Constants below the range, 1024 among them, are unaffected.
const (
	yearRangeLo = 1100
	yearRangeHi = 2099
)

var fourDigitRE = regexp.MustCompile(`\d{4}`)

// expandYears rewrites standalone four-digit years as words.
//
// "Standalone" is the whole safety property. A four-digit run touching a hyphen,
// slash, colon, letter or another digit is part of something else — an ISO date,
// a phone number, a range, a version, an identifier — and is left alone. Without
// that rule "2026-08-11" becomes "twenty twenty-six-08-eleven".
func expandYears(s string) string {
	matches := fourDigitRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}

	var b strings.Builder
	last := 0
	for _, loc := range matches {
		start, end := loc[0], loc[1]
		if !isStandaloneToken(s, start, end) {
			continue
		}
		n, err := strconv.Atoi(s[start:end])
		if err != nil || n < yearRangeLo || n > yearRangeHi {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(yearWords(n))
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isStandaloneToken reports whether s[start:end] is delimited by whitespace,
// brackets, quotes or sentence punctuation rather than being glued to something.
func isStandaloneToken(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if !unicode.IsSpace(r) && !strings.ContainsRune(`([{"'`, r) {
			return false
		}
	}
	if end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if !unicode.IsSpace(r) && !strings.ContainsRune(`)]}"',.!?;`, r) {
			return false
		}
		// "1999.5" is a decimal, not a year followed by a full stop.
		if r == '.' {
			if next, _ := utf8.DecodeRuneInString(s[end+size:]); unicode.IsDigit(next) {
				return false
			}
		}
	}
	return true
}

func yearWords(n int) string {
	hi, lo := n/100, n%100
	switch {
	case n == 2000:
		return "two thousand"
	case lo == 0:
		return cardinal(hi) + " hundred"
	case hi == 20 && lo < 10:
		// 2005 is "two thousand five", not "twenty oh five".
		return "two thousand " + ones[lo]
	case lo < 10:
		return cardinal(hi) + " oh " + ones[lo]
	default:
		return cardinal(hi) + " " + cardinal(lo)
	}
}

var ones = [...]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
	"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen",
	"seventeen", "eighteen", "nineteen",
}

var tensWords = [...]string{
	"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety",
}

// cardinal renders 0-99 for the two halves of a year reading. It is not a
// general number-to-words function on purpose — see the package doc above for
// why general expansion was removed.
func cardinal(n int) string {
	switch {
	case n < 20:
		return ones[n]
	case n%10 == 0:
		return tensWords[n/10]
	default:
		return tensWords[n/10] + "-" + ones[n%10]
	}
}
