package vim

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Columns throughout this package are byte offsets into a line — the view and
// the host editor index the same way. Every column the buffer produces or
// accepts is snapped to a rune boundary, and every edit spans whole runes, so a
// prompt containing em dashes, curly quotes or accented names can't be sliced
// into invalid UTF-8.

// runeStart snaps col down to the start of the rune that contains it.
func runeStart(line string, col int) int {
	if col <= 0 {
		return 0
	}
	if col >= len(line) {
		return len(line)
	}
	for col > 0 && !utf8.RuneStart(line[col]) {
		col--
	}
	return col
}

// runeEnd returns the byte offset just past the rune beginning at col.
func runeEnd(line string, col int) int {
	col = runeStart(line, col)
	if col >= len(line) {
		return len(line)
	}
	_, size := utf8.DecodeRuneInString(line[col:])
	return col + size
}

// prevRuneStart returns the byte offset of the rune before col.
func prevRuneStart(line string, col int) int {
	col = runeStart(line, col)
	if col <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(line[:col])
	return col - size
}

// lastRuneStart returns the byte offset of the final rune on a line, or 0 when
// the line is empty. This is the normal-mode "on the last character" column.
func lastRuneStart(line string) int {
	if line == "" {
		return 0
	}
	return prevRuneStart(line, len(line))
}

// Buffer provides vim-style operations on a text buffer.
type Buffer struct {
	lines  []string
	cursor Position
	yank   string // Yanked text (clipboard)
}

// Position represents a cursor position in the buffer.
type Position struct {
	Line int
	Col  int
}

// NewBuffer creates a new buffer from text content.
func NewBuffer(content string) *Buffer {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	return &Buffer{
		lines:  lines,
		cursor: Position{Line: 0, Col: 0},
	}
}

// Content returns the buffer content as a single string.
func (b *Buffer) Content() string {
	return strings.Join(b.lines, "\n")
}

// Lines returns the buffer lines.
func (b *Buffer) Lines() []string {
	return b.lines
}

// Cursor returns the current cursor position.
func (b *Buffer) Cursor() Position {
	return b.cursor
}

// SetCursor sets the cursor position with bounds checking.
// In normal mode, cursor is clamped to lineLen-1 (on last char).
func (b *Buffer) SetCursor(pos Position) {
	b.setCursor(pos, false)
}

// SetCursorInsert sets cursor position for insert mode (allows col == lineLen).
func (b *Buffer) SetCursorInsert(pos Position) {
	b.setCursor(pos, true)
}

// setCursor is the internal cursor setter.
func (b *Buffer) setCursor(pos Position, insertMode bool) {
	if pos.Line < 0 {
		pos.Line = 0
	}
	if pos.Line >= len(b.lines) {
		pos.Line = len(b.lines) - 1
	}
	if pos.Line < 0 {
		pos.Line = 0
	}

	line := b.lines[pos.Line]
	if pos.Col < 0 {
		pos.Col = 0
	}

	if insertMode {
		// In insert mode, cursor can be at end of line (for appending)
		if pos.Col > len(line) {
			pos.Col = len(line)
		}
	} else if pos.Col >= len(line) {
		// In normal mode, cursor rests on the last character
		pos.Col = lastRuneStart(line)
	}
	pos.Col = runeStart(line, pos.Col)

	b.cursor = pos
}

// CursorOffset returns the absolute offset of the cursor in the content.
func (b *Buffer) CursorOffset() int {
	offset := 0
	for i := 0; i < b.cursor.Line; i++ {
		offset += len(b.lines[i]) + 1 // +1 for newline
	}
	offset += b.cursor.Col
	return offset
}

// SetCursorFromOffset sets cursor position from absolute offset.
func (b *Buffer) SetCursorFromOffset(offset int) {
	currentOffset := 0
	for i, line := range b.lines {
		lineEnd := currentOffset + len(line)
		if offset <= lineEnd || i == len(b.lines)-1 {
			col := offset - currentOffset
			if col > len(line) {
				col = len(line)
			}
			if col < 0 {
				col = 0
			}
			b.cursor.Line = i
			b.cursor.Col = runeStart(line, col)
			return
		}
		currentOffset = lineEnd + 1 // +1 for newline
	}
}

// CurrentLine returns the current line.
func (b *Buffer) CurrentLine() string {
	if b.cursor.Line >= 0 && b.cursor.Line < len(b.lines) {
		return b.lines[b.cursor.Line]
	}
	return ""
}

// CurrentLineLen returns the length of the current line.
func (b *Buffer) CurrentLineLen() int {
	return len(b.CurrentLine())
}

// CharUnderCursor returns the character under the cursor.
func (b *Buffer) CharUnderCursor() rune {
	line := b.CurrentLine()
	if b.cursor.Col >= 0 && b.cursor.Col < len(line) {
		r, _ := utf8.DecodeRuneInString(line[runeStart(line, b.cursor.Col):])
		return r
	}
	return 0
}

// LineCount returns the number of lines.
func (b *Buffer) LineCount() int {
	return len(b.lines)
}

// Insert inserts text at the current cursor position.
func (b *Buffer) Insert(text string) {
	if len(b.lines) == 0 {
		b.lines = []string{""}
	}

	line := b.lines[b.cursor.Line]
	col := b.cursor.Col
	if col > len(line) {
		col = len(line)
	}

	before := line[:col]
	after := line[col:]

	// Handle multi-line insert
	insertLines := strings.Split(text, "\n")
	if len(insertLines) == 1 {
		b.lines[b.cursor.Line] = before + text + after
		b.cursor.Col += len(text)
	} else {
		// Multi-line insert
		firstLine := before + insertLines[0]
		lastLine := insertLines[len(insertLines)-1] + after

		newLines := make([]string, 0, len(b.lines)+len(insertLines)-1)
		newLines = append(newLines, b.lines[:b.cursor.Line]...)
		newLines = append(newLines, firstLine)
		newLines = append(newLines, insertLines[1:len(insertLines)-1]...)
		newLines = append(newLines, lastLine)
		newLines = append(newLines, b.lines[b.cursor.Line+1:]...)

		b.lines = newLines
		b.cursor.Line += len(insertLines) - 1
		b.cursor.Col = len(insertLines[len(insertLines)-1])
	}
}

// DeleteChar deletes the character under the cursor, joining with the next line
// at end of line. This is <Del> semantics; normal-mode x uses DeleteCharNoJoin.
func (b *Buffer) DeleteChar() string {
	line := b.CurrentLine()
	if b.cursor.Col >= len(line) {
		// At end of line, join with next line
		if b.cursor.Line < len(b.lines)-1 {
			b.lines[b.cursor.Line] = line + b.lines[b.cursor.Line+1]
			b.lines = append(b.lines[:b.cursor.Line+1], b.lines[b.cursor.Line+2:]...)
			return "\n"
		}
		return ""
	}

	start := runeStart(line, b.cursor.Col)
	end := runeEnd(line, start)
	deleted := line[start:end]
	b.lines[b.cursor.Line] = line[:start] + line[end:]
	b.cursor.Col = start
	return deleted
}

// DeleteCharNoJoin deletes the character under the cursor and stops at the line
// boundary — vim's x never pulls the next line up, and on an empty line it is a
// no-op.
func (b *Buffer) DeleteCharNoJoin() string {
	line := b.CurrentLine()
	if b.cursor.Col >= len(line) {
		return ""
	}
	return b.DeleteChar()
}

// DeleteCharBefore deletes the character before the cursor (backspace).
func (b *Buffer) DeleteCharBefore() string {
	line := b.CurrentLine()
	if b.cursor.Col > len(line) {
		b.cursor.Col = len(line)
	}
	if b.cursor.Col > 0 {
		start := prevRuneStart(line, b.cursor.Col)
		end := runeStart(line, b.cursor.Col)
		deleted := line[start:end]
		b.lines[b.cursor.Line] = line[:start] + line[end:]
		b.cursor.Col = start
		return deleted
	}

	// At start of line, join with previous line
	if b.cursor.Line > 0 {
		prevLine := b.lines[b.cursor.Line-1]
		b.lines[b.cursor.Line-1] = prevLine + b.CurrentLine()
		b.lines = append(b.lines[:b.cursor.Line], b.lines[b.cursor.Line+1:]...)
		b.cursor.Line--
		b.cursor.Col = len(prevLine)
		return "\n"
	}

	return ""
}

// DeleteLine deletes the current line.
func (b *Buffer) DeleteLine() string {
	if len(b.lines) == 0 {
		return ""
	}

	deleted := b.lines[b.cursor.Line] + "\n"

	if len(b.lines) == 1 {
		b.lines = []string{""}
		b.cursor.Col = 0
	} else {
		b.lines = append(b.lines[:b.cursor.Line], b.lines[b.cursor.Line+1:]...)
		if b.cursor.Line >= len(b.lines) {
			b.cursor.Line = len(b.lines) - 1
		}
	}

	// Move to first non-blank character
	b.cursor.Col = b.firstNonBlank(b.cursor.Line)
	return deleted
}

// DeleteToEndOfLine deletes from cursor to end of line.
func (b *Buffer) DeleteToEndOfLine() string {
	line := b.CurrentLine()
	if b.cursor.Col >= len(line) {
		return ""
	}
	deleted := line[b.cursor.Col:]
	b.lines[b.cursor.Line] = line[:b.cursor.Col]
	if b.cursor.Col > 0 {
		b.cursor.Col = prevRuneStart(b.lines[b.cursor.Line], b.cursor.Col)
	}
	return deleted
}

// normalizeSpan orders two positions, clamps them to the buffer, and resolves
// the end into an exclusive byte column. Inclusive spans (e, $, %, text objects)
// cover the rune at end; exclusive spans (w, b, h, l, 0, ^) stop before it.
func (b *Buffer) normalizeSpan(start, end Position, inclusive bool) (Position, Position) {
	if start.Line > end.Line || (start.Line == end.Line && start.Col > end.Col) {
		start, end = end, start
	}
	start.Line = min(max(start.Line, 0), len(b.lines)-1)
	end.Line = min(max(end.Line, 0), len(b.lines)-1)

	startLine := b.lines[start.Line]
	start.Col = runeStart(startLine, min(max(start.Col, 0), len(startLine)))

	endLine := b.lines[end.Line]
	endCol := min(max(end.Col, 0), len(endLine))
	if inclusive {
		endCol = runeEnd(endLine, endCol)
	} else {
		endCol = runeStart(endLine, endCol)
	}
	if start.Line == end.Line && endCol < start.Col {
		endCol = start.Col
	}
	end.Col = endCol
	return start, end
}

// DeleteRange deletes text between two positions, including the rune at end.
func (b *Buffer) DeleteRange(start, end Position) string {
	return b.deleteSpan(start, end, true)
}

// DeleteRangeExclusive deletes text between two positions, stopping before the
// rune at end — the shape vim's exclusive motions (w, b, h, l, 0, ^) use.
func (b *Buffer) DeleteRangeExclusive(start, end Position) string {
	return b.deleteSpan(start, end, false)
}

func (b *Buffer) deleteSpan(start, end Position, inclusive bool) string {
	from, to := b.normalizeSpan(start, end, inclusive)

	if from.Line == to.Line {
		line := b.lines[from.Line]
		deleted := line[from.Col:to.Col]
		b.lines[from.Line] = line[:from.Col] + line[to.Col:]
		b.cursor = from
		b.clampCursor()
		return deleted
	}

	var deleted strings.Builder
	firstLine := b.lines[from.Line]
	deleted.WriteString(firstLine[from.Col:])
	deleted.WriteString("\n")
	for i := from.Line + 1; i < to.Line; i++ {
		deleted.WriteString(b.lines[i])
		deleted.WriteString("\n")
	}
	lastLine := b.lines[to.Line]
	deleted.WriteString(lastLine[:to.Col])

	newLines := make([]string, 0, len(b.lines)-(to.Line-from.Line))
	newLines = append(newLines, b.lines[:from.Line]...)
	newLines = append(newLines, firstLine[:from.Col]+lastLine[to.Col:])
	newLines = append(newLines, b.lines[to.Line+1:]...)
	b.lines = newLines

	b.cursor = from
	b.clampCursor()
	return deleted.String()
}

// DeleteLines removes whole lines [from, to] and returns them with trailing
// newlines, so linewise operators (dd, dj, Vd) round-trip through the yank
// register the way vim's linewise register does.
func (b *Buffer) DeleteLines(from, to int) string {
	from, to = clampLineRange(from, to, len(b.lines))
	deleted := strings.Join(b.lines[from:to+1], "\n") + "\n"

	remaining := make([]string, 0, len(b.lines)-(to-from+1))
	remaining = append(remaining, b.lines[:from]...)
	remaining = append(remaining, b.lines[to+1:]...)
	if len(remaining) == 0 {
		remaining = []string{""}
	}
	b.lines = remaining

	b.cursor.Line = min(from, len(b.lines)-1)
	b.cursor.Col = b.firstNonBlank(b.cursor.Line)
	return deleted
}

// YankLines copies whole lines [from, to] into the yank register.
func (b *Buffer) YankLines(from, to int) string {
	from, to = clampLineRange(from, to, len(b.lines))
	b.yank = strings.Join(b.lines[from:to+1], "\n") + "\n"
	return b.yank
}

func clampLineRange(from, to, count int) (int, int) {
	if from > to {
		from, to = to, from
	}
	from = min(max(from, 0), count-1)
	to = min(max(to, 0), count-1)
	return from, to
}

// clampCursor re-snaps the cursor after an edit shortened its line.
func (b *Buffer) clampCursor() {
	b.setCursor(b.cursor, false)
}

// YankRange copies text between two positions to the yank register, including
// the rune at end.
func (b *Buffer) YankRange(start, end Position) string {
	return b.yankSpan(start, end, true)
}

// YankRangeExclusive copies text between two positions, stopping before the
// rune at end.
func (b *Buffer) YankRangeExclusive(start, end Position) string {
	return b.yankSpan(start, end, false)
}

func (b *Buffer) yankSpan(start, end Position, inclusive bool) string {
	from, to := b.normalizeSpan(start, end, inclusive)

	if from.Line == to.Line {
		b.yank = b.lines[from.Line][from.Col:to.Col]
		return b.yank
	}

	var yanked strings.Builder
	yanked.WriteString(b.lines[from.Line][from.Col:])
	yanked.WriteString("\n")
	for i := from.Line + 1; i < to.Line; i++ {
		yanked.WriteString(b.lines[i])
		yanked.WriteString("\n")
	}
	yanked.WriteString(b.lines[to.Line][:to.Col])

	b.yank = yanked.String()
	return b.yank
}

// YankLine copies the current line to the yank register.
func (b *Buffer) YankLine() string {
	b.yank = b.CurrentLine() + "\n"
	return b.yank
}

// Paste inserts yanked text after the cursor.
func (b *Buffer) Paste() {
	if b.yank == "" {
		return
	}

	// If yank ends with newline, paste on next line
	if strings.HasSuffix(b.yank, "\n") {
		content := strings.TrimSuffix(b.yank, "\n")
		newLines := strings.Split(content, "\n")

		insertAt := b.cursor.Line + 1
		combined := make([]string, 0, len(b.lines)+len(newLines))
		combined = append(combined, b.lines[:insertAt]...)
		combined = append(combined, newLines...)
		combined = append(combined, b.lines[insertAt:]...)
		b.lines = combined

		b.cursor.Line = insertAt
		b.cursor.Col = b.firstNonBlank(insertAt)
	} else {
		// Paste inline after the rune under the cursor
		b.cursor.Col = runeEnd(b.CurrentLine(), b.cursor.Col)
		b.Insert(b.yank)
	}
}

// PasteBefore inserts yanked text before the cursor.
func (b *Buffer) PasteBefore() {
	if b.yank == "" {
		return
	}

	if strings.HasSuffix(b.yank, "\n") {
		content := strings.TrimSuffix(b.yank, "\n")
		newLines := strings.Split(content, "\n")

		insertAt := b.cursor.Line
		combined := make([]string, 0, len(b.lines)+len(newLines))
		combined = append(combined, b.lines[:insertAt]...)
		combined = append(combined, newLines...)
		combined = append(combined, b.lines[insertAt:]...)
		b.lines = combined

		b.cursor.Col = b.firstNonBlank(insertAt)
	} else {
		b.Insert(b.yank)
	}
}

// ReplaceChar replaces the character under cursor.
func (b *Buffer) ReplaceChar(r rune) {
	line := b.CurrentLine()
	if b.cursor.Col >= len(line) {
		return
	}
	start := runeStart(line, b.cursor.Col)
	b.lines[b.cursor.Line] = line[:start] + string(r) + line[runeEnd(line, start):]
	b.cursor.Col = start
}

// JoinLines joins the current line with the next.
func (b *Buffer) JoinLines() {
	if b.cursor.Line >= len(b.lines)-1 {
		return
	}

	currentLine := b.lines[b.cursor.Line]
	nextLine := strings.TrimLeft(b.lines[b.cursor.Line+1], " \t")

	joinCol := len(currentLine)
	if joinCol > 0 && !strings.HasSuffix(currentLine, " ") {
		b.lines[b.cursor.Line] = currentLine + " " + nextLine
		joinCol++
	} else {
		b.lines[b.cursor.Line] = currentLine + nextLine
	}

	b.lines = append(b.lines[:b.cursor.Line+1], b.lines[b.cursor.Line+2:]...)
	b.cursor.Col = joinCol
}

// NewLine inserts a new line below and moves cursor.
func (b *Buffer) NewLineBelow() {
	newLines := make([]string, 0, len(b.lines)+1)
	newLines = append(newLines, b.lines[:b.cursor.Line+1]...)
	newLines = append(newLines, "")
	newLines = append(newLines, b.lines[b.cursor.Line+1:]...)
	b.lines = newLines
	b.cursor.Line++
	b.cursor.Col = 0
}

// NewLineAbove inserts a new line above and moves cursor.
func (b *Buffer) NewLineAbove() {
	newLines := make([]string, 0, len(b.lines)+1)
	newLines = append(newLines, b.lines[:b.cursor.Line]...)
	newLines = append(newLines, "")
	newLines = append(newLines, b.lines[b.cursor.Line:]...)
	b.lines = newLines
	b.cursor.Col = 0
}

// firstNonBlank returns the column of the first non-blank character on a line.
func (b *Buffer) firstNonBlank(line int) int {
	if line < 0 || line >= len(b.lines) {
		return 0
	}
	for i, r := range b.lines[line] {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return 0
}

// FirstNonBlank moves cursor to first non-blank character on current line.
func (b *Buffer) FirstNonBlank() {
	b.cursor.Col = b.firstNonBlank(b.cursor.Line)
}

// SetContent replaces the entire buffer content.
func (b *Buffer) SetContent(content string) {
	b.lines = strings.Split(content, "\n")
	if len(b.lines) == 0 {
		b.lines = []string{""}
	}
	// Ensure cursor is valid
	b.clampCursor()
}

// Yank returns the current yank register content.
func (b *Buffer) Yank() string {
	return b.yank
}

// SetYank sets the yank register content.
func (b *Buffer) SetYank(content string) {
	b.yank = content
}
