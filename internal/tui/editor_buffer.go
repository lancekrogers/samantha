package tui

import (
	"unicode"
)

// editorBuffer is the UI-independent state for the composer. It owns text
// coordinates, selection, the unnamed register, and undo history; Bubble Tea
// only mirrors this state into its textarea widget.
type editorBuffer struct {
	value     string
	cursor    int
	selection editorSelection
	register  editorRegister
	undo      []editorSnapshot
}

type editorSelection struct {
	active   bool
	anchor   int
	linewise bool
}

type editorRegister struct {
	text     string
	linewise bool
}

type editorSnapshot struct {
	value  string
	cursor int
}

type editorMotion int

const (
	motionLeft editorMotion = iota
	motionRight
	motionDown
	motionUp
	motionLineStart
	motionLineEnd
	motionFirstText
	motionWordForward
	motionWordBackward
	motionWordEnd
	motionFirst
	motionLast
)

func newEditorBuffer(value string) editorBuffer {
	return editorBuffer{value: value}
}

func (b *editorBuffer) sync(value string, cursor int) {
	cursor = clampOffset(cursor, runeLen(value))
	if b.value == value && b.cursor == cursor {
		return
	}
	b.value = value
	b.cursor = cursor
	b.selection = editorSelection{}
}

func (b editorBuffer) text() string {
	return b.value
}

func (b editorBuffer) length() int {
	return runeLen(b.value)
}

func (b editorBuffer) cursorOffset() int {
	return clampOffset(b.cursor, b.length())
}

func (b *editorBuffer) setCursor(offset int) {
	b.cursor = clampOffset(offset, b.length())
}

func (b editorBuffer) lineBounds(offset int) (int, int) {
	runes := []rune(b.value)
	offset = clampOffset(offset, len(runes))
	start := offset
	for start > 0 && runes[start-1] != '\n' {
		start--
	}
	end := offset
	for end < len(runes) && runes[end] != '\n' {
		end++
	}
	return start, end
}

func (b editorBuffer) lineStart(offset int) int {
	start, _ := b.lineBounds(offset)
	return start
}

func (b *editorBuffer) move(motion editorMotion) {
	position := b.cursorOffset()
	switch motion {
	case motionLeft:
		b.setCursor(position - 1)
	case motionRight:
		if b.length() > 0 {
			b.setCursor(min(position+1, b.length()-1))
		}
	case motionDown, motionUp:
		b.moveVertical(motion == motionDown)
	case motionLineStart:
		b.setCursor(b.lineStart(position))
	case motionLineEnd:
		_, end := b.lineBounds(position)
		if end > b.lineStart(position) {
			end--
		}
		b.setCursor(end)
	case motionFirstText:
		start, end := b.lineBounds(position)
		runes := []rune(b.value)
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		b.setCursor(start)
	case motionWordForward:
		b.setCursor(b.nextWord(position))
	case motionWordBackward:
		b.setCursor(b.previousWord(position))
	case motionWordEnd:
		b.setCursor(b.endWord(position))
	case motionFirst:
		b.setCursor(0)
	case motionLast:
		b.setCursor(max(b.length()-1, 0))
	}
}

func (b *editorBuffer) moveVertical(down bool) {
	runes := []rune(b.value)
	position := b.cursorOffset()
	start, _ := b.lineBounds(position)
	column := position - start
	_, currentEnd := b.lineBounds(position)
	target := currentEnd
	if down {
		if currentEnd >= len(runes) {
			return
		}
		target = currentEnd + 1
		_, targetEnd := b.lineBounds(target)
		target = min(target+column, targetEnd)
	} else {
		if start == 0 {
			return
		}
		previousStart := b.lineStart(start - 1)
		_, previousEnd := b.lineBounds(previousStart)
		target = min(previousStart+column, previousEnd)
	}
	b.setCursor(target)
}

type editorFindDirection int

const (
	findForward editorFindDirection = iota
	findBackward
	findTillForward
	findTillBackward
)

func (b *editorBuffer) find(direction editorFindDirection, target rune) bool {
	runes := []rune(b.value)
	position := b.cursorOffset()
	switch direction {
	case findForward, findTillForward:
		for i := position + 1; i < len(runes) && runes[i] != '\n'; i++ {
			if runes[i] == target {
				if direction == findTillForward {
					i--
				}
				b.setCursor(i)
				return true
			}
		}
	case findBackward, findTillBackward:
		for i := position - 1; i >= 0 && runes[i] != '\n'; i-- {
			if runes[i] == target {
				if direction == findTillBackward {
					i++
				}
				b.setCursor(i)
				return true
			}
		}
	}
	return false
}

func (b editorBuffer) nextWord(position int) int {
	runes := []rune(b.value)
	position = clampOffset(position, len(runes))
	for position < len(runes) && unicode.IsSpace(runes[position]) {
		position++
	}
	for position < len(runes) && !unicode.IsSpace(runes[position]) {
		position++
	}
	for position < len(runes) && unicode.IsSpace(runes[position]) {
		position++
	}
	return min(position, max(len(runes)-1, 0))
}

func (b editorBuffer) previousWord(position int) int {
	runes := []rune(b.value)
	position = min(max(position-1, 0), len(runes))
	for position > 0 && unicode.IsSpace(runes[position]) {
		position--
	}
	for position > 0 && !unicode.IsSpace(runes[position-1]) {
		position--
	}
	return position
}

func (b editorBuffer) endWord(position int) int {
	runes := []rune(b.value)
	position = clampOffset(position, len(runes))
	for position < len(runes) && unicode.IsSpace(runes[position]) {
		position++
	}
	for position+1 < len(runes) && !unicode.IsSpace(runes[position+1]) {
		position++
	}
	return min(position, max(len(runes)-1, 0))
}

func (b *editorBuffer) beginSelection(linewise bool) {
	b.selection = editorSelection{active: true, anchor: b.cursorOffset(), linewise: linewise}
}

func (b *editorBuffer) selectionActive() bool {
	return b.selection.active
}

func (b *editorBuffer) selectionLinewise() bool {
	return b.selection.linewise
}

func (b *editorBuffer) toggleSelectionLinewise() {
	b.selection.linewise = !b.selection.linewise
}

func (b *editorBuffer) clearSelection() {
	b.selection = editorSelection{}
}

func (b *editorBuffer) swapSelectionAnchor() {
	if !b.selection.active {
		return
	}
	current := b.cursorOffset()
	anchor := b.selection.anchor
	b.selection.anchor = current
	b.setCursor(anchor)
}

func (b editorBuffer) selectedRange() (int, int, bool) {
	if !b.selection.active {
		return 0, 0, false
	}
	current := b.cursorOffset()
	if b.selection.linewise {
		start := b.lineStart(b.selection.anchor)
		other := b.lineStart(current)
		if other < start {
			start, other = other, start
		}
		_, end := b.lineBounds(other)
		if end < b.length() {
			end++
		}
		return start, end, end > start
	}
	start, end := b.selection.anchor, current
	if start > end {
		start, end = end, start
	}
	end = min(end+1, b.length())
	return start, end, end > start
}

func (b editorBuffer) selectedText() string {
	start, end, ok := b.selectedRange()
	if !ok {
		return ""
	}
	runes := []rune(b.value)
	return string(runes[start:end])
}

func (b *editorBuffer) selectAll() {
	b.selection = editorSelection{active: true, anchor: 0}
	b.setCursor(b.length())
}

func (b *editorBuffer) deleteSelection() (string, bool) {
	start, end, ok := b.selectedRange()
	if !ok {
		b.clearSelection()
		return "", false
	}
	text, changed := b.deleteRange(start, end, b.selection.linewise)
	b.clearSelection()
	return text, changed
}

func (b *editorBuffer) deleteRange(start, end int, linewise bool) (string, bool) {
	runes := []rune(b.value)
	start = clampOffset(start, len(runes))
	end = clampOffset(end, len(runes))
	if start > end {
		start, end = end, start
	}
	if start == end {
		return "", false
	}
	b.pushUndo()
	deleted := string(runes[start:end])
	b.register = editorRegister{text: deleted, linewise: linewise}
	runes = append(runes[:start:start], runes[end:]...)
	b.value = string(runes)
	b.setCursor(start)
	b.clearSelection()
	return deleted, true
}

func (b *editorBuffer) insertAt(offset int, text string, replaceSelection bool) {
	runes := []rune(b.value)
	if replaceSelection {
		if start, end, ok := b.selectedRange(); ok {
			offset = start
			runes = append(runes[:start:start], runes[end:]...)
			b.clearSelection()
		}
	}
	offset = clampOffset(offset, len(runes))
	b.pushUndo()
	insert := []rune(text)
	runes = append(runes[:offset:offset], append(insert, runes[offset:]...)...)
	b.value = string(runes)
	b.setCursor(offset + len(insert))
}

func (b *editorBuffer) replaceRange(start, end int, text string) {
	runes := []rune(b.value)
	start = clampOffset(start, len(runes))
	end = clampOffset(end, len(runes))
	if start > end {
		start, end = end, start
	}
	b.pushUndo()
	insert := []rune(text)
	runes = append(runes[:start:start], append(insert, runes[end:]...)...)
	b.value = string(runes)
	b.setCursor(start + len(insert))
	b.clearSelection()
}

func (b *editorBuffer) registerValue() editorRegister {
	return b.register
}

func (b *editorBuffer) setRegister(text string, linewise bool) {
	b.register = editorRegister{text: text, linewise: linewise}
}

func (b *editorBuffer) pushUndo() {
	b.undo = append(b.undo, editorSnapshot{value: b.value, cursor: b.cursorOffset()})
	if len(b.undo) > 100 {
		b.undo = append([]editorSnapshot(nil), b.undo[len(b.undo)-100:]...)
	}
}

func (b *editorBuffer) resetUndo() {
	b.undo = nil
}

func (b *editorBuffer) undoEdit() bool {
	if len(b.undo) == 0 {
		return false
	}
	snapshot := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	b.value = snapshot.value
	b.cursor = snapshot.cursor
	b.clearSelection()
	return true
}

func runeLen(value string) int {
	return len([]rune(value))
}

func clampOffset(offset, length int) int {
	return min(max(offset, 0), length)
}
