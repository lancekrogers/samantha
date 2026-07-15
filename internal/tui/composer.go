package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

type vimInputMode int

const (
	vimInsert vimInputMode = iota
	vimNormal
	vimVisual
)

type composerSnapshot struct {
	value string
	row   int
	col   int
}

func (m *conversationModel) updateComposer(msg tea.KeyMsg) tea.Cmd {
	if m.selectionActive && m.vimMode != vimVisual && isReplacingKey(msg) {
		m.deleteSelection(false)
		switch msg.String() {
		case "backspace", "delete":
			return nil
		}
	}
	previous := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncComposer(previous)
	return cmd
}

func (m *conversationModel) syncComposer(previous string) {
	query := commandToken(m.input.Value())
	if query != m.commandQuery {
		m.commandQuery = query
		m.commandSelection = 0
	}
	if matches := matchingSlashCommands(m.input.Value()); len(matches) == 0 {
		m.commandSelection = 0
	} else if m.commandSelection >= len(matches) {
		m.commandSelection = len(matches) - 1
	}
	if previous != m.input.Value() || m.commandPaletteRows() > 0 {
		m.reflow()
	}
}

func (m *conversationModel) handleCommandPaletteKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	matches := matchingSlashCommands(m.input.Value())
	if len(matches) == 0 {
		return false, nil
	}
	switch msg.String() {
	case "tab":
		selected := matches[min(m.commandSelection, len(matches)-1)]
		previous := m.input.Value()
		m.input.SetValue(selected.name + " ")
		m.commandSelection = 0
		m.syncComposer(previous)
		return true, nil
	case "up", "shift+tab":
		m.commandSelection = (m.commandSelection - 1 + len(matches)) % len(matches)
		return true, nil
	case "down":
		m.commandSelection = (m.commandSelection + 1) % len(matches)
		return true, nil
	}
	return false, nil
}

func (m conversationModel) commandPaletteRows() int {
	matches := matchingSlashCommands(m.input.Value())
	if len(matches) == 0 || m.height < 13 {
		return 0
	}
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Keep at least three transcript rows. Above that floor, show every match
	// that actually fits instead of imposing an arbitrary item cap.
	available := m.height - inputHeight - 6 - 3
	return min(len(matches), max(available, 0))
}

func (m conversationModel) renderCommandPalette() string {
	rows := m.commandPaletteRows()
	if rows == 0 {
		return ""
	}
	matches := matchingSlashCommands(m.input.Value())
	selection := min(m.commandSelection, len(matches)-1)
	start := 0
	if selection >= rows {
		start = selection - rows + 1
	}
	end := min(start+rows, len(matches))
	lines := make([]string, 0, rows)
	for i, command := range matches[start:end] {
		index := start + i
		prefix := "  "
		style := dimStyle
		if index == selection {
			prefix = "› "
			style = selectedStyle
		}
		line := fmt.Sprintf("%s%-24s %s", prefix, command.usage, command.description)
		lines = append(lines, style.Render(ansi.Truncate(line, max(m.width, 1), "…")))
	}
	return strings.Join(lines, "\n")
}

func (m *conversationModel) configureVim(args []string) {
	if len(args) > 1 {
		m.commandError("usage: /vim [on|off|insert]")
		return
	}
	action := "toggle"
	if len(args) == 1 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "toggle":
		m.vimEnabled = !m.vimEnabled
	case "on", "normal":
		m.vimEnabled = true
	case "insert":
		m.vimEnabled = true
		m.vimMode = vimInsert
	case "off":
		m.vimEnabled = false
	default:
		m.commandError("usage: /vim [on|off|insert]")
		return
	}
	if m.vimEnabled {
		if action != "insert" {
			m.vimMode = vimNormal
		}
		m.vimPending = ""
		m.commandNotice("Vim input enabled. Press i to insert; Esc returns to NORMAL.")
	} else {
		m.vimMode = vimInsert
		m.vimPending = ""
		m.clearSelection()
		m.commandNotice("Vim input disabled.")
	}
}

func (m *conversationModel) enterVimInsert() {
	m.pushVimUndo()
	m.vimMode = vimInsert
	m.vimPending = ""
	_ = m.input.Focus()
}

func (m *conversationModel) enterVimNormal() {
	m.vimMode = vimNormal
	m.vimPending = ""
	m.clearSelection()
	if m.cursorColumn() > 0 {
		m.updateComposer(tea.KeyMsg{Type: tea.KeyLeft})
	}
}

func (m *conversationModel) handleVimNormalKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if strings.HasPrefix(m.vimPending, "operator:") {
		return m.finishVimOperatorFind(key)
	}
	if strings.HasPrefix(m.vimPending, "find:") {
		return m.finishFindMotion(key)
	}
	if strings.HasPrefix(m.vimPending, "replace:") {
		return m.replaceCurrentRune(key)
	}
	if m.vimPending == "g" {
		m.vimPending = ""
		if key == "g" {
			m.moveCursorToOffset(0)
			return nil
		}
	}
	if m.vimPending == "d" || m.vimPending == "c" {
		return m.finishVimOperator(key)
	}

	switch key {
	case "/":
		// Slash commands are application commands, so make `/` a convenient
		// escape hatch from NORMAL into INSERT rather than silently ignoring it.
		m.enterVimInsert()
		return m.updateComposer(msg)
	case "i":
		m.enterVimInsert()
	case "a":
		m.enterVimInsert()
		if m.cursorColumn() < m.currentLineLength() {
			return m.updateComposer(tea.KeyMsg{Type: tea.KeyRight})
		}
	case "I":
		m.input.CursorStart()
		m.enterVimInsert()
	case "A":
		m.input.CursorEnd()
		m.enterVimInsert()
	case "h", "left", "l", "right", "j", "down", "k", "up", "0", "home", "$", "end", "^", "w", "b", "e", "G":
		m.moveVimMotion(key)
	case "g":
		m.vimPending = "g"
	case "f", "F", "t", "T":
		m.vimPending = "find:" + key
	case "x", "delete":
		m.deleteVimRange(m.cursorOffset(), min(m.cursorOffset()+1, m.composerLength()), false, false)
	case "X":
		cursor := m.cursorOffset()
		if cursor > 0 {
			m.deleteVimRange(cursor-1, cursor, false, false)
		}
	case "D":
		m.deleteToLineEnd(false)
	case "C":
		m.deleteToLineEnd(true)
	case "d":
		m.vimPending = "d"
	case "c":
		m.vimPending = "c"
	case "r":
		m.vimPending = "replace:"
	case "v":
		m.beginVisual(false)
	case "V":
		m.beginVisual(true)
	case "p":
		return m.pasteRegister(true)
	case "P":
		return m.pasteRegister(false)
	case "J":
		m.joinLineBelow()
	case "o":
		m.pushVimUndo()
		m.openVimLine(true)
		m.vimMode = vimInsert
	case "O":
		m.pushVimUndo()
		m.openVimLine(false)
		m.vimMode = vimInsert
	case "u":
		m.undoVimEdit()
	case "~":
		m.toggleCurrentRuneCase()
	case "esc":
		m.vimPending = ""
	}
	return nil
}

func (m *conversationModel) handleVimVisualKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.vimPending == "g" {
		m.vimPending = ""
		if key == "g" {
			m.moveCursorToOffset(0)
		}
		return nil
	}
	switch key {
	case "esc":
		m.enterVimNormal()
	case "v":
		m.enterVimNormal()
	case "V":
		m.selectionLinewise = !m.selectionLinewise
	case "h", "left", "l", "right", "j", "down", "k", "up", "0", "home", "$", "end", "^", "w", "b", "e", "G":
		m.moveVimMotion(key)
	case "g":
		m.vimPending = "g"
	case "y":
		m.copySelection()
		m.enterVimNormal()
	case "d", "x", "delete":
		m.deleteSelection(false)
		m.enterVimNormal()
	case "c":
		m.deleteSelection(true)
	case "o":
		m.swapSelectionAnchor()
	case "p", "P":
		text, linewise := m.vimRegister, m.vimRegisterLine
		if text == "" {
			text, linewise = m.selectedText(), m.selectionLinewise
		}
		m.deleteSelection(false)
		m.vimRegister, m.vimRegisterLine = text, linewise
		return m.pasteRegister(false)
	}
	return nil
}

func (m *conversationModel) finishVimOperator(key string) tea.Cmd {
	op := m.vimPending
	if key == op {
		m.vimPending = ""
		m.pushVimUndo()
		m.deleteVimLine()
		if op == "c" {
			m.vimMode = vimInsert
		}
		return nil
	}
	if key == "f" || key == "F" || key == "t" || key == "T" {
		m.vimPending = "operator:" + op + ":find:" + key
		return nil
	}
	if key == "w" || key == "b" || key == "e" {
		start := m.cursorOffset()
		m.moveVimMotion(key)
		end := m.cursorOffset()
		if end > start {
			if key == "e" {
				end = min(end+1, m.composerLength())
			}
			m.deleteVimRange(start, end, op == "c", false)
		} else if end < start {
			m.deleteVimRange(end, min(start+1, m.composerLength()), op == "c", false)
		}
		m.vimPending = ""
		return nil
	}
	if key == "$" {
		m.vimPending = ""
		m.deleteToLineEnd(op == "c")
		return nil
	}
	if key == "0" {
		start := m.cursorOffset()
		m.moveVimMotion(key)
		m.deleteVimRange(m.cursorOffset(), start, op == "c", false)
		m.vimPending = ""
		return nil
	}
	m.vimPending = ""
	return nil
}

func (m *conversationModel) finishFindMotion(key string) tea.Cmd {
	pending := m.vimPending
	m.vimPending = ""
	if len([]rune(key)) != 1 {
		return nil
	}
	parts := strings.SplitN(pending, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	m.findRune(parts[1], []rune(key)[0])
	return nil
}

func (m *conversationModel) finishVimOperatorFind(key string) tea.Cmd {
	parts := strings.Split(m.vimPending, ":")
	if len(parts) != 4 || len([]rune(key)) != 1 {
		m.vimPending = ""
		return nil
	}
	op, direction := parts[1], parts[3]
	start := m.cursorOffset()
	m.vimPending = ""
	m.findRune(direction, []rune(key)[0])
	target := m.cursorOffset()
	if target == start {
		return nil
	}
	end := target
	if target > start {
		if direction == "f" {
			end++
		}
		m.deleteVimRange(start, min(end, m.composerLength()), op == "c", false)
		return nil
	}
	if direction == "F" {
		start = max(target, 0)
	}
	m.deleteVimRange(start, min(target+1, m.composerLength()), op == "c", false)
	return nil
}

func (m *conversationModel) moveVimMotion(key string) {
	if strings.HasPrefix(m.vimPending, "operator:") {
		return
	}
	if key == "g" && m.vimPending == "g" {
		m.moveCursorToOffset(0)
		m.vimPending = ""
		return
	}
	m.vimPending = ""
	switch key {
	case "h", "left":
		m.moveCursorToOffset(max(m.cursorOffset()-1, 0))
	case "l", "right":
		m.moveCursorToOffset(min(m.cursorOffset()+1, max(m.composerLength()-1, 0)))
	case "j", "down":
		m.input.CursorDown()
	case "k", "up":
		m.input.CursorUp()
	case "0", "home":
		m.moveCursorToOffset(m.lineStartOffset(m.cursorOffset()))
	case "^":
		start, end := m.lineBounds(m.cursorOffset())
		runes := []rune(m.input.Value())
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		m.moveCursorToOffset(start)
	case "$", "end":
		_, end := m.lineBounds(m.cursorOffset())
		if end > m.lineStartOffset(m.cursorOffset()) {
			end--
		}
		m.moveCursorToOffset(end)
	case "w":
		m.moveCursorToOffset(m.nextWordOffset(m.cursorOffset()))
	case "b":
		m.moveCursorToOffset(m.previousWordOffset(m.cursorOffset()))
	case "e":
		m.moveCursorToOffset(m.endWordOffset(m.cursorOffset()))
	case "G":
		m.moveCursorToOffset(max(m.composerLength()-1, 0))
	}
}

func (m *conversationModel) findRune(direction string, target rune) {
	runes := []rune(m.input.Value())
	pos := m.cursorOffset()
	if direction == "f" || direction == "t" {
		for i := pos + 1; i < len(runes); i++ {
			if runes[i] == target {
				if direction == "t" {
					i--
				}
				m.moveCursorToOffset(i)
				return
			}
			if runes[i] == '\n' {
				break
			}
		}
		return
	}
	for i := pos - 1; i >= 0 && runes[i] != '\n'; i-- {
		if runes[i] == target {
			if direction == "T" {
				i++
			}
			m.moveCursorToOffset(i)
			return
		}
	}
}

func (m *conversationModel) nextWordOffset(pos int) int {
	runes := []rune(m.input.Value())
	pos = min(max(pos, 0), len(runes))
	for pos < len(runes) && unicode.IsSpace(runes[pos]) {
		pos++
	}
	for pos < len(runes) && !unicode.IsSpace(runes[pos]) {
		pos++
	}
	for pos < len(runes) && unicode.IsSpace(runes[pos]) {
		pos++
	}
	return min(pos, max(len(runes)-1, 0))
}

func (m *conversationModel) previousWordOffset(pos int) int {
	runes := []rune(m.input.Value())
	pos = min(max(pos-1, 0), len(runes))
	for pos > 0 && unicode.IsSpace(runes[pos]) {
		pos--
	}
	for pos > 0 && !unicode.IsSpace(runes[pos-1]) {
		pos--
	}
	return pos
}

func (m *conversationModel) endWordOffset(pos int) int {
	runes := []rune(m.input.Value())
	pos = min(max(pos, 0), len(runes))
	for pos < len(runes) && unicode.IsSpace(runes[pos]) {
		pos++
	}
	for pos+1 < len(runes) && !unicode.IsSpace(runes[pos+1]) {
		pos++
	}
	return min(pos, max(len(runes)-1, 0))
}

func (m *conversationModel) replaceCurrentRune(key string) tea.Cmd {
	m.vimPending = ""
	runes := []rune(key)
	if len(runes) != 1 {
		return nil
	}
	value := []rune(m.input.Value())
	pos := m.cursorOffset()
	if pos >= len(value) {
		return nil
	}
	m.pushVimUndo()
	value[pos] = runes[0]
	m.input.SetValue(string(value))
	m.moveCursorToOffset(pos)
	return nil
}

func (m *conversationModel) toggleCurrentRuneCase() {
	value := []rune(m.input.Value())
	pos := m.cursorOffset()
	if pos >= len(value) {
		return
	}
	m.pushVimUndo()
	if unicode.IsUpper(value[pos]) {
		value[pos] = unicode.ToLower(value[pos])
	} else {
		value[pos] = unicode.ToUpper(value[pos])
	}
	m.input.SetValue(string(value))
	m.moveCursorToOffset(pos)
}

func (m *conversationModel) joinLineBelow() {
	row := m.input.Line()
	lines := strings.Split(m.input.Value(), "\n")
	if row >= len(lines)-1 {
		return
	}
	m.pushVimUndo()
	col := m.cursorColumn()
	lines[row] = strings.TrimRightFunc(lines[row], unicode.IsSpace) + " " + strings.TrimLeftFunc(lines[row+1], unicode.IsSpace)
	lines = append(lines[:row+1], lines[row+2:]...)
	m.input.SetValue(strings.Join(lines, "\n"))
	m.moveCursorTo(min(row, len(lines)-1), min(col, len([]rune(lines[row]))-1))
}

type clipboardPasteMsg struct {
	text string
	err  error
}

func readClipboard() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.ReadAll()
		return clipboardPasteMsg{text: text, err: err}
	}
}

func (m conversationModel) composerLength() int {
	return len([]rune(m.input.Value()))
}

func (m conversationModel) cursorOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := min(max(m.input.Line(), 0), len(lines)-1)
	offset := 0
	for i := 0; i < row; i++ {
		offset += len([]rune(lines[i])) + 1
	}
	return min(offset+min(max(m.cursorColumn(), 0), len([]rune(lines[row]))), m.composerLength())
}

func (m conversationModel) lineBounds(offset int) (int, int) {
	runes := []rune(m.input.Value())
	offset = min(max(offset, 0), len(runes))
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

func (m conversationModel) lineStartOffset(offset int) int {
	start, _ := m.lineBounds(offset)
	return start
}

func (m *conversationModel) moveCursorToOffset(offset int) {
	runes := []rune(m.input.Value())
	offset = min(max(offset, 0), len(runes))
	row, col := 0, 0
	for _, r := range runes[:offset] {
		if r == '\n' {
			row++
			col = 0
			continue
		}
		col++
	}
	m.moveCursorTo(row, col)
}

func (m *conversationModel) beginVisual(linewise bool) {
	m.vimMode = vimVisual
	m.selectionActive = true
	m.selectionAnchor = m.cursorOffset()
	m.selectionLinewise = linewise
	m.vimPending = ""
}

func (m *conversationModel) swapSelectionAnchor() {
	if !m.selectionActive {
		return
	}
	current := m.cursorOffset()
	m.selectionAnchor, current = current, m.selectionAnchor
	m.moveCursorToOffset(current)
}

func (m *conversationModel) clearSelection() {
	m.selectionActive = false
	m.selectionAnchor = 0
	m.selectionLinewise = false
}

func (m conversationModel) selectedRange() (int, int, bool) {
	if !m.selectionActive {
		return 0, 0, false
	}
	current := m.cursorOffset()
	if m.selectionLinewise {
		start := m.lineStartOffset(m.selectionAnchor)
		other := m.lineStartOffset(current)
		if other < start {
			start, other = other, start
		}
		_, end := m.lineBounds(other)
		if end < m.composerLength() {
			end++
		}
		return start, end, end > start
	}
	start, end := m.selectionAnchor, current
	if start > end {
		start, end = end, start
	}
	end = min(end+1, m.composerLength())
	return start, end, end > start
}

func (m conversationModel) selectedText() string {
	start, end, ok := m.selectedRange()
	if !ok {
		return ""
	}
	runes := []rune(m.input.Value())
	return string(runes[start:end])
}

func (m *conversationModel) selectAll() {
	m.selectionActive = true
	m.selectionAnchor = 0
	m.selectionLinewise = false
	m.moveCursorToOffset(m.composerLength())
}

func (m *conversationModel) deleteSelection(enterInsert bool) {
	start, end, ok := m.selectedRange()
	if !ok {
		m.clearSelection()
		return
	}
	text := m.selectedText()
	m.vimRegister = text
	m.vimRegisterLine = m.selectionLinewise
	m.deleteVimRange(start, end, enterInsert, m.selectionLinewise)
	m.clearSelection()
}

func (m *conversationModel) deleteVimRange(start, end int, enterInsert, linewise bool) {
	runes := []rune(m.input.Value())
	start = min(max(start, 0), len(runes))
	end = min(max(end, 0), len(runes))
	if start > end {
		start, end = end, start
	}
	if start == end {
		return
	}
	m.pushVimUndo()
	m.vimRegister = string(runes[start:end])
	m.vimRegisterLine = linewise
	previous := m.input.Value()
	runes = append(runes[:start:start], runes[end:]...)
	m.input.SetValue(string(runes))
	if len(runes) == 0 {
		m.input.Reset()
	} else {
		m.moveCursorToOffset(min(start, len(runes)))
	}
	m.syncComposer(previous)
	if enterInsert {
		m.vimMode = vimInsert
		_ = m.input.Focus()
	}
}

func (m *conversationModel) deleteToLineEnd(enterInsert bool) {
	start := m.cursorOffset()
	_, end := m.lineBounds(start)
	if end > start {
		m.deleteVimRange(start, end, enterInsert, false)
	}
}

func (m *conversationModel) copySelection() {
	text := m.selectedText()
	if text == "" {
		return
	}
	m.vimRegister = text
	m.vimRegisterLine = m.selectionLinewise
	if err := clipboard.WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Copied %d characters", len([]rune(text))))
}

func (m *conversationModel) cutSelection() {
	text := m.selectedText()
	if text == "" {
		return
	}
	m.vimRegister = text
	m.vimRegisterLine = m.selectionLinewise
	err := clipboard.WriteAll(text)
	m.deleteSelection(false)
	if err != nil {
		m.commandError("cut: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Cut %d characters", len([]rune(text))))
}

func (m *conversationModel) insertClipboardText(text string) {
	if text == "" {
		return
	}
	if start, _, ok := m.selectedRange(); ok {
		m.insertTextAt(start, text, true)
		return
	}
	m.insertTextAt(m.cursorOffset(), text, false)
}

func (m *conversationModel) pasteRegister(after bool) tea.Cmd {
	if m.vimRegister == "" {
		text, err := clipboard.ReadAll()
		if err != nil {
			m.commandError("paste failed: " + err.Error())
			return nil
		}
		m.vimRegister = text
		m.vimRegisterLine = strings.Contains(text, "\n")
	}
	text := m.vimRegister
	offset := m.cursorOffset()
	if m.vimRegisterLine {
		_, lineEnd := m.lineBounds(offset)
		if after {
			offset = lineEnd
			if offset < m.composerLength() {
				offset++
			}
		} else {
			offset = m.lineStartOffset(offset)
		}
		if !strings.HasSuffix(text, "\n") && offset < m.composerLength() {
			text += "\n"
		}
	} else if after && offset < m.composerLength() {
		offset++
	}
	m.insertTextAt(offset, text, false)
	return nil
}

func (m *conversationModel) insertTextAt(offset int, text string, replaceSelection bool) {
	runes := []rune(m.input.Value())
	if replaceSelection {
		if start, end, ok := m.selectedRange(); ok {
			offset = start
			runes = append(runes[:start:start], runes[end:]...)
			m.clearSelection()
		}
	}
	offset = min(max(offset, 0), len(runes))
	m.pushVimUndo()
	insert := []rune(text)
	runes = append(runes[:offset:offset], append(insert, runes[offset:]...)...)
	previous := m.input.Value()
	m.input.SetValue(string(runes))
	m.moveCursorToOffset(offset + len(insert))
	m.syncComposer(previous)
}

func isReplacingKey(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyRunes {
		return len(msg.Runes) > 0
	}
	switch msg.String() {
	case "backspace", "delete", "ctrl+j", "ctrl+enter", "alt+enter", "shift+enter":
		return true
	default:
		return false
	}
}

func (m conversationModel) cursorColumn() int {
	info := m.input.LineInfo()
	return info.StartColumn + info.ColumnOffset
}

func (m conversationModel) currentLineLength() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := min(m.input.Line(), len(lines)-1)
	return len([]rune(lines[row]))
}

func (m *conversationModel) pushVimUndo() {
	m.vimUndo = append(m.vimUndo, composerSnapshot{
		value: m.input.Value(), row: m.input.Line(), col: m.cursorColumn(),
	})
	if len(m.vimUndo) > 100 {
		m.vimUndo = append([]composerSnapshot(nil), m.vimUndo[len(m.vimUndo)-100:]...)
	}
}

func (m *conversationModel) undoVimEdit() {
	if len(m.vimUndo) == 0 {
		return
	}
	snapshot := m.vimUndo[len(m.vimUndo)-1]
	m.vimUndo = m.vimUndo[:len(m.vimUndo)-1]
	previous := m.input.Value()
	m.input.SetValue(snapshot.value)
	m.moveCursorTo(snapshot.row, snapshot.col)
	m.syncComposer(previous)
}

func (m *conversationModel) deleteVimLine() {
	previous := m.input.Value()
	lines := strings.Split(previous, "\n")
	row := min(m.input.Line(), len(lines)-1)
	if len(lines) == 1 {
		m.input.Reset()
		m.syncComposer(previous)
		return
	}
	lines = append(lines[:row], lines[row+1:]...)
	m.input.SetValue(strings.Join(lines, "\n"))
	m.moveCursorTo(min(row, len(lines)-1), 0)
	m.syncComposer(previous)
}

func (m *conversationModel) openVimLine(below bool) {
	previous := m.input.Value()
	lines := strings.Split(previous, "\n")
	row := min(m.input.Line(), len(lines)-1)
	insertAt := row
	if below {
		insertAt++
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = ""
	m.input.SetValue(strings.Join(lines, "\n"))
	m.moveCursorTo(insertAt, 0)
	m.syncComposer(previous)
}

func (m *conversationModel) moveCursorTo(row, col int) {
	// Textarea keeps row/column private, but its public input-begin motion and
	// logical-line accessor let us position deterministically across wraps.
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	limit := m.input.LineCount() + len([]rune(m.input.Value())) + 1
	for m.input.Line() < row && limit > 0 {
		m.input.CursorDown()
		limit--
	}
	m.input.SetCursor(col)
}

func (m conversationModel) vimFooterHelp() string {
	if !m.vimEnabled {
		return "enter send  ^J newline  ^G mic  ^O audio  ^T switch  PgUp/PgDn scroll"
	}
	if m.vimMode == vimInsert {
		return "INSERT  esc normal  enter send  ^J newline  tab complete"
	}
	if m.vimMode == vimVisual {
		return "VISUAL  hjkl move  y yank  d delete  c change  o swap  esc normal"
	}
	pending := ""
	if m.vimPending != "" {
		pending = "  pending " + m.vimPending
	}
	return "NORMAL" + pending + "  i/a insert  0/$/w/b move  v/V select  y/p copy/paste  x/dd delete  u undo  / commands"
}

func (m conversationModel) vimCompactFooterHelp() string {
	if m.vimMode == vimInsert {
		return "INSERT  esc normal  enter send  tab complete"
	}
	if m.vimMode == vimVisual {
		return "VISUAL  hjkl move  y yank  d delete  c change  esc normal"
	}
	pending := ""
	if m.vimPending != "" {
		pending = " " + m.vimPending
	}
	return "NORMAL" + pending + "  i/a insert  hjkl move  v select  y/p copy/paste  x/dd delete"
}

func (m conversationModel) vimInputLabel(label string) string {
	if !m.vimEnabled {
		return label
	}
	mode := "INSERT"
	color := lipgloss.Color("10")
	if m.vimMode == vimNormal {
		mode = "NORMAL"
		color = lipgloss.Color("14")
	} else if m.vimMode == vimVisual {
		mode = "VISUAL"
		color = lipgloss.Color("13")
	}
	badge := lipgloss.NewStyle().Bold(true).Foreground(color).Render(mode)
	return badge + "  " + label
}
