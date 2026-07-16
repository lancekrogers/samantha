package tui

import tea "github.com/charmbracelet/bubbletea"

func (m *conversationModel) enterVimInsert() {
	m.editor.pushUndo()
	m.vim.mode = vimInsert
	m.vim.clearPending()
	_ = m.input.Focus()
}

func (m *conversationModel) enterVimNormal() {
	m.vim.mode = vimNormal
	m.vim.clearPending()
	m.editor.clearSelection()
	if m.cursorOffset() > m.editor.lineStart(m.cursorOffset()) {
		m.editor.move(motionLeft)
		m.applyEditor(m.input.Value())
	}
}

func (m *conversationModel) handleVimNormalKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.vim.pending.kind == vimPendingOperatorFind {
		return m.finishVimOperatorFind(key)
	}
	if m.vim.pending.kind == vimPendingFind {
		return m.finishFindMotion(key)
	}
	if m.vim.pending.kind == vimPendingReplace {
		return m.replaceCurrentRune(key)
	}
	if m.vim.pending.kind == vimPendingG {
		m.vim.clearPending()
		if key == "g" {
			m.editor.move(motionFirst)
			m.applyEditor(m.input.Value())
			return nil
		}
	}
	if m.vim.pending.kind == vimPendingOperator {
		return m.finishVimOperator(key)
	}

	switch key {
	case "/":
		m.enterVimInsert()
		return m.updateComposer(msg)
	case "i":
		m.enterVimInsert()
	case "a":
		m.enterVimInsert()
		if m.editor.cursorOffset() < m.currentLineLength() {
			m.editor.move(motionRight)
			m.applyEditor(m.input.Value())
		}
	case "I":
		m.editor.move(motionLineStart)
		m.enterVimInsert()
		m.applyEditor(m.input.Value())
	case "A":
		m.editor.move(motionLineEnd)
		m.enterVimInsert()
		m.applyEditor(m.input.Value())
	case "h", "left", "l", "right", "j", "down", "k", "up", "0", "home", "$", "end", "^", "w", "b", "e", "G":
		m.moveVimMotion(key)
	case "g":
		m.vim.pending = vimPending{kind: vimPendingG}
	case "f", "F", "t", "T":
		m.vim.pending = vimPending{kind: vimPendingFind, direction: key[0]}
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
		m.vim.pending = vimPending{kind: vimPendingOperator, operator: 'd'}
	case "c":
		m.vim.pending = vimPending{kind: vimPendingOperator, operator: 'c'}
	case "r":
		m.vim.pending = vimPending{kind: vimPendingReplace}
	case "v":
		m.editor.beginSelection(false)
		m.vim.mode = vimVisual
	case "V":
		m.editor.beginSelection(true)
		m.vim.mode = vimVisual
	case "p":
		return m.pasteRegister(true)
	case "P":
		return m.pasteRegister(false)
	case "J":
		m.joinLineBelow()
	case "o":
		m.openVimLine(true)
		m.vim.mode = vimInsert
	case "O":
		m.openVimLine(false)
		m.vim.mode = vimInsert
	case "u":
		m.undoVimEdit()
	case "~":
		m.toggleCurrentRuneCase()
	case "esc":
		m.vim.clearPending()
	}
	return nil
}

func (m *conversationModel) handleVimVisualKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.vim.pending.kind == vimPendingG {
		m.vim.clearPending()
		if key == "g" {
			m.editor.move(motionFirst)
			m.applyEditor(m.input.Value())
		}
		return nil
	}
	switch key {
	case "esc", "v":
		m.enterVimNormal()
	case "V":
		m.editor.toggleSelectionLinewise()
	case "h", "left", "l", "right", "j", "down", "k", "up", "0", "home", "$", "end", "^", "w", "b", "e", "G":
		m.moveVimMotion(key)
	case "g":
		m.vim.pending = vimPending{kind: vimPendingG}
	case "y":
		m.copySelection()
		m.enterVimNormal()
	case "d", "x", "delete":
		m.deleteSelection(false)
		m.enterVimNormal()
	case "c":
		m.deleteSelection(true)
	case "o":
		m.editor.swapSelectionAnchor()
		m.moveCursorToOffset(m.editor.cursorOffset())
	case "p", "P":
		register := m.editor.registerValue()
		if register.text == "" {
			register = editorRegister{text: m.editor.selectedText(), linewise: m.editor.selectionLinewise()}
		}
		m.deleteSelection(false)
		m.editor.setRegister(register.text, register.linewise)
		return m.pasteRegister(false)
	}
	return nil
}

func (m *conversationModel) moveVimMotion(key string) {
	motion, ok := vimMotionForKey(key)
	if !ok {
		return
	}
	m.editor.move(motion)
	m.applyEditor(m.input.Value())
}

func vimMotionForKey(key string) (editorMotion, bool) {
	switch key {
	case "h", "left":
		return motionLeft, true
	case "l", "right":
		return motionRight, true
	case "j", "down":
		return motionDown, true
	case "k", "up":
		return motionUp, true
	case "0", "home":
		return motionLineStart, true
	case "$", "end":
		return motionLineEnd, true
	case "^":
		return motionFirstText, true
	case "w":
		return motionWordForward, true
	case "b":
		return motionWordBackward, true
	case "e":
		return motionWordEnd, true
	case "G":
		return motionLast, true
	default:
		return 0, false
	}
}

func (m *conversationModel) finishVimOperator(key string) tea.Cmd {
	pending := m.vim.pending
	if key == string(pending.operator) {
		m.vim.clearPending()
		m.deleteVimLine()
		if pending.operator == 'c' {
			m.vim.mode = vimInsert
		}
		return nil
	}
	if key == "f" || key == "F" || key == "t" || key == "T" {
		m.vim.pending = vimPending{kind: vimPendingOperatorFind, operator: pending.operator, direction: key[0]}
		return nil
	}
	if motion, ok := vimMotionForKey(key); ok {
		start := m.cursorOffset()
		m.editor.move(motion)
		end := m.cursorOffset()
		if end > start {
			if motion == motionWordEnd {
				end = min(end+1, m.composerLength())
			}
			m.deleteVimRange(start, end, pending.operator == 'c', false)
		} else if end < start {
			m.deleteVimRange(end, min(start+1, m.composerLength()), pending.operator == 'c', false)
		}
		m.vim.clearPending()
		return nil
	}
	m.vim.clearPending()
	return nil
}

func (m *conversationModel) finishFindMotion(key string) tea.Cmd {
	pending := m.vim.pending
	m.vim.clearPending()
	runes := []rune(key)
	if len(runes) != 1 {
		return nil
	}
	direction := map[byte]editorFindDirection{
		'f': findForward, 'F': findBackward, 't': findTillForward, 'T': findTillBackward,
	}[pending.direction]
	m.editor.find(direction, runes[0])
	m.applyEditor(m.input.Value())
	return nil
}

func (m *conversationModel) finishVimOperatorFind(key string) tea.Cmd {
	pending := m.vim.pending
	m.vim.clearPending()
	runes := []rune(key)
	if len(runes) != 1 {
		return nil
	}
	direction := map[byte]editorFindDirection{
		'f': findForward, 'F': findBackward, 't': findTillForward, 'T': findTillBackward,
	}[pending.direction]
	start := m.cursorOffset()
	if !m.editor.find(direction, runes[0]) {
		return nil
	}
	target := m.cursorOffset()
	end := target
	if target > start && pending.direction == 'f' {
		end++
	}
	if target < start && pending.direction == 'F' {
		start = target
	}
	m.deleteVimRange(min(start, target), min(max(end, target+1), m.composerLength()), pending.operator == 'c', false)
	return nil
}

func (m *conversationModel) replaceCurrentRune(key string) tea.Cmd {
	m.vim.clearPending()
	runes := []rune(key)
	position := m.cursorOffset()
	if len(runes) != 1 || position >= m.composerLength() {
		return nil
	}
	previous := m.input.Value()
	m.editor.replaceRange(position, position+1, string(runes[0]))
	m.applyEditor(previous)
	return nil
}

func (m *conversationModel) toggleCurrentRuneCase() {
	position := m.cursorOffset()
	runes := []rune(m.editor.text())
	if position >= len(runes) {
		return
	}
	if runes[position] >= 'a' && runes[position] <= 'z' {
		runes[position] -= 'a' - 'A'
	} else if runes[position] >= 'A' && runes[position] <= 'Z' {
		runes[position] += 'a' - 'A'
	} else {
		return
	}
	previous := m.input.Value()
	m.editor.replaceRange(position, position+1, string(runes[position]))
	m.applyEditor(previous)
}

func (m *conversationModel) deleteVimRange(start, end int, enterInsert, linewise bool) {
	previous := m.input.Value()
	_, changed := m.editor.deleteRange(start, end, linewise)
	if !changed {
		return
	}
	m.applyEditor(previous)
	if enterInsert {
		m.vim.mode = vimInsert
		_ = m.input.Focus()
	}
}

func (m *conversationModel) deleteSelection(enterInsert bool) {
	previous := m.input.Value()
	_, changed := m.editor.deleteSelection()
	if !changed {
		return
	}
	m.applyEditor(previous)
	if enterInsert {
		m.vim.mode = vimInsert
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

func (m *conversationModel) deleteVimLine() {
	position := m.cursorOffset()
	start, end := m.lineBounds(position)
	if end < m.composerLength() {
		end++
	} else if start > 0 {
		start--
	}
	m.deleteVimRange(start, end, false, true)
}

func (m *conversationModel) openVimLine(below bool) {
	position := m.cursorOffset()
	start, end := m.lineBounds(position)
	offset := start
	if below {
		offset = end
		if offset < m.composerLength() {
			offset++
		}
	}
	previous := m.input.Value()
	m.editor.insertAt(offset, "\n", false)
	m.applyEditor(previous)
}

func (m *conversationModel) joinLineBelow() {
	position := m.cursorOffset()
	_, end := m.lineBounds(position)
	if end >= m.composerLength() {
		return
	}
	runes := []rune(m.editor.text())
	next := end + 1
	for next < len(runes) && (runes[next] == ' ' || runes[next] == '\t') {
		next++
	}
	previous := m.input.Value()
	m.editor.replaceRange(end, next, " ")
	m.applyEditor(previous)
}

func (m *conversationModel) undoVimEdit() {
	previous := m.input.Value()
	if m.editor.undoEdit() {
		m.applyEditor(previous)
	}
}
