package vim

import (
	"unicode"
	"unicode/utf8"
)

// Motion represents the result of a motion command.
//
// Linewise motions (j, k, gg, G, {, }) make an operator act on whole lines.
// Exclusive motions (w, W, b, B, h, l, 0, ^) stop before the rune the cursor
// landed on; inclusive motions (e, E, $, %, f/t) cover it. vim draws the same
// distinction, and it is what makes d0 and db delete the right span.
type Motion struct {
	Start     Position
	End       Position
	Linewise  bool
	Exclusive bool
}

// MoveLeft moves cursor left by count characters.
func MoveLeft(b *Buffer, count int) Motion {
	start := b.Cursor()
	line := b.CurrentLine()
	col := start.Col
	for range count {
		if col <= 0 {
			break
		}
		col = prevRuneStart(line, col)
	}
	b.SetCursor(Position{Line: start.Line, Col: col})
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveRight moves cursor right by count characters.
func MoveRight(b *Buffer, count int) Motion {
	start := b.Cursor()
	line := b.CurrentLine()
	last := lastRuneStart(line)
	col := start.Col
	for range count {
		if col >= last {
			break
		}
		col = runeEnd(line, col)
	}
	b.SetCursor(Position{Line: start.Line, Col: col})
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveDown moves cursor down by count lines.
func MoveDown(b *Buffer, count int) Motion {
	start := b.Cursor()
	line := start.Line + count
	if line >= b.LineCount() {
		line = b.LineCount() - 1
	}
	if line < 0 {
		line = 0
	}
	b.SetCursor(Position{Line: line, Col: start.Col})
	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// MoveUp moves cursor up by count lines.
func MoveUp(b *Buffer, count int) Motion {
	start := b.Cursor()
	line := start.Line - count
	if line < 0 {
		line = 0
	}
	b.SetCursor(Position{Line: line, Col: start.Col})
	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// MoveToLineStart moves cursor to start of line (column 0).
func MoveToLineStart(b *Buffer) Motion {
	start := b.Cursor()
	b.SetCursor(Position{Line: start.Line, Col: 0})
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveToLineEnd moves cursor to end of line.
func MoveToLineEnd(b *Buffer) Motion {
	start := b.Cursor()
	b.SetCursor(Position{Line: start.Line, Col: lastRuneStart(b.CurrentLine())})
	return Motion{Start: start, End: b.Cursor()}
}

// MoveToFirstNonBlank moves cursor to first non-blank character.
func MoveToFirstNonBlank(b *Buffer) Motion {
	start := b.Cursor()
	b.FirstNonBlank()
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveWordForward moves cursor forward by count words.
func MoveWordForward(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset < len(content); i++ {
		offset = nextWordStart(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveWordBackward moves cursor backward by count words.
func MoveWordBackward(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset > 0; i++ {
		offset = prevWordStart(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveWordEnd moves cursor to end of current/next word.
func MoveWordEnd(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset < len(content)-1; i++ {
		offset = wordEnd(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor()}
}

// MoveBigWordForward moves cursor forward by count WORDs (whitespace-delimited).
func MoveBigWordForward(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset < len(content); i++ {
		offset = nextBigWordStart(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveBigWordBackward moves cursor backward by count WORDs.
func MoveBigWordBackward(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset > 0; i++ {
		offset = prevBigWordStart(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor(), Exclusive: true}
}

// MoveBigWordEnd moves cursor to end of current/next WORD.
func MoveBigWordEnd(b *Buffer, count int) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	for i := 0; i < count && offset < len(content)-1; i++ {
		offset = bigWordEnd(content, offset)
	}

	b.SetCursorFromOffset(offset)
	return Motion{Start: start, End: b.Cursor()}
}

// MoveToDocumentStart moves cursor to start of document.
func MoveToDocumentStart(b *Buffer) Motion {
	start := b.Cursor()
	b.SetCursor(Position{Line: 0, Col: 0})
	b.FirstNonBlank()
	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// MoveToDocumentEnd moves cursor to last line of document.
func MoveToDocumentEnd(b *Buffer) Motion {
	start := b.Cursor()
	b.SetCursor(Position{Line: b.LineCount() - 1, Col: 0})
	b.FirstNonBlank()
	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// MoveToLine moves cursor to a specific line number (1-indexed).
func MoveToLine(b *Buffer, lineNum int) Motion {
	start := b.Cursor()
	line := lineNum - 1 // Convert to 0-indexed
	if line < 0 {
		line = 0
	}
	if line >= b.LineCount() {
		line = b.LineCount() - 1
	}
	b.SetCursor(Position{Line: line, Col: 0})
	b.FirstNonBlank()
	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// FindCharForward finds the count-th occurrence of char forward.
func FindCharForward(b *Buffer, char rune, count int, till bool) Motion {
	start := b.Cursor()
	line := b.CurrentLine()

	found := 0
	for i := runeEnd(line, start.Col); i < len(line); i = stepFwd(line, i) {
		if runeAt(line, i) == char {
			found++
			if found == count {
				col := i
				if till {
					col = stepBack(line, i) // Stop before the character
				}
				b.SetCursor(Position{Line: start.Line, Col: col})
				return Motion{Start: start, End: b.Cursor()}
			}
		}
	}

	// Not found, don't move
	return Motion{Start: start, End: start}
}

// FindCharBackward finds the count-th occurrence of char backward.
func FindCharBackward(b *Buffer, char rune, count int, till bool) Motion {
	start := b.Cursor()
	line := b.CurrentLine()

	found := 0
	for i := start.Col; i > 0; {
		i = stepBack(line, i)
		if runeAt(line, i) == char {
			found++
			if found == count {
				col := i
				if till {
					col = stepFwd(line, i) // Stop after the character
				}
				b.SetCursor(Position{Line: start.Line, Col: col})
				return Motion{Start: start, End: b.Cursor()}
			}
		}
	}

	// Not found, don't move
	return Motion{Start: start, End: start}
}

// MoveParagraphForward moves to start of next paragraph.
func MoveParagraphForward(b *Buffer, count int) Motion {
	start := b.Cursor()

	for range count {
		line := b.Cursor().Line
		// Skip current non-empty lines
		for line < b.LineCount()-1 && len(b.Lines()[line]) > 0 {
			line++
		}
		// Skip empty lines
		for line < b.LineCount()-1 && len(b.Lines()[line]) == 0 {
			line++
		}
		b.SetCursor(Position{Line: line, Col: 0})
	}

	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// MoveParagraphBackward moves to start of previous paragraph.
func MoveParagraphBackward(b *Buffer, count int) Motion {
	start := b.Cursor()

	for range count {
		line := b.Cursor().Line
		// Skip current empty lines
		for line > 0 && len(b.Lines()[line]) == 0 {
			line--
		}
		// Skip non-empty lines
		for line > 0 && len(b.Lines()[line]) > 0 {
			line--
		}
		b.SetCursor(Position{Line: line, Col: 0})
	}

	return Motion{Start: start, End: b.Cursor(), Linewise: true}
}

// Helper functions for word navigation. Offsets are byte offsets into content;
// every step advances or retreats by a whole rune so multi-byte text (em dashes,
// curly quotes, accented names) is classified and never split.

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// runeAt decodes the rune starting at offset.
func runeAt(content string, offset int) rune {
	if offset < 0 || offset >= len(content) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(content[offset:])
	return r
}

// stepFwd returns the offset of the next rune after offset.
func stepFwd(content string, offset int) int {
	if offset >= len(content) {
		return len(content)
	}
	_, size := utf8.DecodeRuneInString(content[offset:])
	return offset + size
}

// stepBack returns the offset of the rune before offset.
func stepBack(content string, offset int) int {
	if offset <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(content[:offset])
	return offset - size
}

func nextWordStart(content string, offset int) int {
	n := len(content)
	if offset >= n {
		return n
	}

	if isWordChar(runeAt(content, offset)) {
		for offset < n && isWordChar(runeAt(content, offset)) {
			offset = stepFwd(content, offset)
		}
	} else if !unicode.IsSpace(runeAt(content, offset)) {
		// Skip punctuation
		for offset < n {
			r := runeAt(content, offset)
			if isWordChar(r) || unicode.IsSpace(r) {
				break
			}
			offset = stepFwd(content, offset)
		}
	}

	// Skip whitespace
	for offset < n && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepFwd(content, offset)
	}

	return offset
}

func prevWordStart(content string, offset int) int {
	if offset <= 0 {
		return 0
	}
	offset = stepBack(content, offset)

	// Skip whitespace backward
	for offset > 0 && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepBack(content, offset)
	}

	if isWordChar(runeAt(content, offset)) {
		for offset > 0 && isWordChar(runeAt(content, stepBack(content, offset))) {
			offset = stepBack(content, offset)
		}
		return offset
	}
	for offset > 0 {
		prev := runeAt(content, stepBack(content, offset))
		if isWordChar(prev) || unicode.IsSpace(prev) {
			break
		}
		offset = stepBack(content, offset)
	}
	return offset
}

func wordEnd(content string, offset int) int {
	n := len(content)
	last := lastRuneOffset(content)
	if offset >= last {
		return last
	}
	offset = stepFwd(content, offset)

	// Skip whitespace
	for offset < n && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepFwd(content, offset)
	}

	if isWordChar(runeAt(content, offset)) {
		for offset < last && isWordChar(runeAt(content, stepFwd(content, offset))) {
			offset = stepFwd(content, offset)
		}
		return offset
	}
	for offset < last {
		next := runeAt(content, stepFwd(content, offset))
		if isWordChar(next) || unicode.IsSpace(next) {
			break
		}
		offset = stepFwd(content, offset)
	}
	return offset
}

func nextBigWordStart(content string, offset int) int {
	n := len(content)
	if offset >= n {
		return n
	}

	// Skip current WORD (non-whitespace)
	for offset < n && !unicode.IsSpace(runeAt(content, offset)) {
		offset = stepFwd(content, offset)
	}

	// Skip whitespace
	for offset < n && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepFwd(content, offset)
	}

	return offset
}

func prevBigWordStart(content string, offset int) int {
	if offset <= 0 {
		return 0
	}
	offset = stepBack(content, offset)

	// Skip whitespace backward
	for offset > 0 && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepBack(content, offset)
	}

	// Find start of WORD
	for offset > 0 && !unicode.IsSpace(runeAt(content, stepBack(content, offset))) {
		offset = stepBack(content, offset)
	}

	return offset
}

func bigWordEnd(content string, offset int) int {
	n := len(content)
	last := lastRuneOffset(content)
	if offset >= last {
		return last
	}
	offset = stepFwd(content, offset)

	// Skip whitespace
	for offset < n && unicode.IsSpace(runeAt(content, offset)) {
		offset = stepFwd(content, offset)
	}

	// Move to end of WORD
	for offset < last && !unicode.IsSpace(runeAt(content, stepFwd(content, offset))) {
		offset = stepFwd(content, offset)
	}

	return offset
}

// lastRuneOffset is the offset of the final rune in content, or 0 when empty.
func lastRuneOffset(content string) int {
	if content == "" {
		return 0
	}
	return stepBack(content, len(content))
}

// bracketPairs maps each bracket to its matching counterpart and direction.
var bracketPairs = map[rune]rune{
	'(': ')', ')': '(',
	'{': '}', '}': '{',
	'[': ']', ']': '[',
}

// isOpenBracket returns true for opening brackets.
func isOpenBracket(r rune) bool {
	return r == '(' || r == '{' || r == '['
}

// isBracket returns true for any bracket character.
func isBracket(r rune) bool {
	_, ok := bracketPairs[r]
	return ok
}

// MatchBracket implements vim's % motion: jump to matching bracket.
func MatchBracket(b *Buffer) Motion {
	start := b.Cursor()
	content := b.Content()
	offset := b.CursorOffset()

	ch := b.CharUnderCursor()

	// If not on a bracket, scan forward on current line to find one.
	if !isBracket(ch) {
		line := b.CurrentLine()
		found := false
		for i := start.Col; i < len(line); i = stepFwd(line, i) {
			if r := runeAt(line, i); isBracket(r) {
				// Move cursor to this bracket, then match from there.
				offset = offset + (i - start.Col)
				ch = r
				found = true
				break
			}
		}
		if !found {
			return Motion{Start: start, End: start}
		}
	}

	var matchOffset int
	if isOpenBracket(ch) {
		matchOffset = findMatchForward(content, offset, ch, bracketPairs[ch])
	} else {
		matchOffset = findMatchBackward(content, offset, bracketPairs[ch], ch)
	}

	if matchOffset == -1 {
		return Motion{Start: start, End: start}
	}

	b.SetCursorFromOffset(matchOffset)
	return Motion{Start: start, End: b.Cursor()}
}

// findMatchForward finds the matching close bracket scanning forward.
// Cursor must be on the open bracket at offset.
func findMatchForward(content string, offset int, open, close rune) int {
	depth := 0
	for i := offset; i < len(content); i = stepFwd(content, i) {
		ch := runeAt(content, i)
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findMatchBackward finds the matching open bracket scanning backward.
// Cursor must be on the close bracket at offset.
func findMatchBackward(content string, offset int, open, close rune) int {
	depth := 0
	for i := offset; ; i = stepBack(content, i) {
		switch runeAt(content, i) {
		case close:
			depth++
		case open:
			depth--
			if depth == 0 {
				return i
			}
		}
		if i == 0 {
			return -1
		}
	}
}
