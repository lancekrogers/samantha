package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

// updateComposer is the Bubble Tea adapter around editorBuffer. Textarea is
// still responsible for text-entry ergonomics and cursor blinking, while all
// modal editing operations go through the UI-independent buffer.
func (m *conversationModel) updateComposer(msg tea.KeyMsg) tea.Cmd {
	if m.editor.selectionActive() && m.vim.mode != vimVisual && isReplacingKey(msg) {
		previous := m.input.Value()
		_, _ = m.editor.deleteSelection()
		m.applyEditor(previous)
		if msg.String() == "backspace" || msg.String() == "delete" {
			return nil
		}
	}
	previous := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncEditorFromTextarea()
	m.syncComposer(previous)
	return cmd
}

func (m *conversationModel) syncEditorFromTextarea() {
	m.editor.sync(m.input.Value(), m.textareaCursorOffset())
}

func (m conversationModel) textareaCursorOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := min(max(m.input.Line(), 0), len(lines)-1)
	offset := 0
	for i := 0; i < row; i++ {
		offset += runeLen(lines[i]) + 1
	}
	return min(offset+min(max(m.cursorColumn(), 0), runeLen(lines[row])), runeLen(m.input.Value()))
}

func (m *conversationModel) applyEditor(previous string) {
	m.input.SetValue(m.editor.text())
	m.moveTextareaCursorToOffset(m.editor.cursorOffset())
	m.syncComposer(previous)
}

func (m *conversationModel) moveCursorToOffset(offset int) {
	if m.editor.text() != m.input.Value() {
		m.syncEditorFromTextarea()
	}
	m.editor.setCursor(offset)
	m.moveTextareaCursorToOffset(m.editor.cursorOffset())
}

func (m *conversationModel) moveTextareaCursorToOffset(offset int) {
	runes := []rune(m.input.Value())
	offset = clampOffset(offset, len(runes))
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

func (m conversationModel) cursorOffset() int {
	return m.editor.cursorOffset()
}

func (m conversationModel) composerLength() int {
	return m.editor.length()
}

func (m conversationModel) lineBounds(offset int) (int, int) {
	return m.editor.lineBounds(offset)
}

func (m conversationModel) lineStartOffset(offset int) int {
	return m.editor.lineStart(offset)
}

func (m conversationModel) currentLineLength() int {
	_, end := m.editor.lineBounds(m.editor.cursorOffset())
	return end - m.editor.lineStart(m.editor.cursorOffset())
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
		m.syncEditorFromTextarea()
		m.commandSelection = 0
		m.syncComposer(previous)
		return true, nil
	// Enter is intentionally not handled here: handleSubmit expands the
	// highlighted palette match via expandPaletteSelection, then runs it.
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
		m.vim.enabled = !m.vim.enabled
	case "on", "normal":
		m.vim.enabled = true
	case "insert":
		m.vim.enabled = true
		m.vim.mode = vimInsert
	case "off":
		m.vim.enabled = false
	default:
		m.commandError("usage: /vim [on|off|insert]")
		return
	}
	if m.vim.enabled {
		if action != "insert" {
			m.vim.mode = vimNormal
		}
		m.vim.clearPending()
		m.commandNotice("Vim input enabled. Press i to insert; Esc returns to NORMAL.")
	} else {
		m.vim.mode = vimInsert
		m.vim.clearPending()
		m.editor.clearSelection()
		m.commandNotice("Vim input disabled.")
	}
}

func (m *conversationModel) insertClipboardText(text string) {
	if text == "" {
		return
	}
	previous := m.input.Value()
	m.editor.insertAt(m.editor.cursorOffset(), text, m.editor.selectionActive())
	m.applyEditor(previous)
}

func (m *conversationModel) pasteRegister(after bool) tea.Cmd {
	register := m.editor.registerValue()
	if register.text == "" {
		text, err := m.clipboard().ReadAll()
		if err != nil {
			m.commandError("paste failed: " + err.Error())
			return nil
		}
		register = editorRegister{text: text, linewise: strings.Contains(text, "\n")}
		m.editor.setRegister(register.text, register.linewise)
	}
	offset := m.editor.cursorOffset()
	text := register.text
	if register.linewise {
		_, lineEnd := m.editor.lineBounds(offset)
		if after {
			offset = lineEnd
			if offset < m.composerLength() {
				offset++
			}
		} else {
			offset = m.editor.lineStart(offset)
		}
		if !strings.HasSuffix(text, "\n") && offset < m.composerLength() {
			text += "\n"
		}
	} else if after && offset < m.composerLength() {
		offset++
	}
	previous := m.input.Value()
	m.editor.insertAt(offset, text, false)
	m.applyEditor(previous)
	return nil
}

func (m *conversationModel) selectAll() {
	m.editor.selectAll()
	m.moveCursorToOffset(m.editor.cursorOffset())
}

func (m *conversationModel) copySelection() {
	text := m.editor.selectedText()
	if text == "" {
		return
	}
	m.editor.setRegister(text, m.editor.selectionLinewise())
	if err := m.clipboard().WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Copied %d characters", runeLen(text)))
}

func (m *conversationModel) cutSelection() {
	text := m.editor.selectedText()
	if text == "" {
		return
	}
	m.editor.setRegister(text, m.editor.selectionLinewise())
	err := m.clipboard().WriteAll(text)
	m.deleteSelection(false)
	if err != nil {
		m.commandError("cut: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Cut %d characters", runeLen(text)))
}

func (m conversationModel) vimFooterHelp() string {
	if !m.vim.enabled {
		return "enter send  ^J newline  ^G mic  ^O audio  ^T switch  PgUp/PgDn scroll"
	}
	if m.vim.mode == vimInsert {
		return "INSERT  esc normal  enter send  ^J newline  tab complete"
	}
	if m.vim.mode == vimVisual {
		return "VISUAL  hjkl move  y yank  d delete  c change  o swap  esc normal"
	}
	pending := ""
	if label := m.vim.pendingLabel(); label != "" {
		pending = "  pending " + label
	}
	return "NORMAL" + pending + "  i/a insert  0/$/w/b move  v/V select  y/p copy/paste  x/dd delete  u undo  / commands"
}

func (m conversationModel) vimCompactFooterHelp() string {
	if m.vim.mode == vimInsert {
		return "INSERT  esc normal  enter send  tab complete"
	}
	if m.vim.mode == vimVisual {
		return "VISUAL  hjkl move  y yank  d delete  c change  esc normal"
	}
	pending := ""
	if label := m.vim.pendingLabel(); label != "" {
		pending = " " + label
	}
	return "NORMAL" + pending + "  i/a insert  hjkl move  v select  y/p copy/paste  x/dd delete"
}

func (m conversationModel) vimInputLabel(label string) string {
	if !m.vim.enabled {
		return label
	}
	mode := "INSERT"
	color := lipgloss.Color("10")
	if m.vim.mode == vimNormal {
		mode = "NORMAL"
		color = lipgloss.Color("14")
	} else if m.vim.mode == vimVisual {
		mode = "VISUAL"
		color = lipgloss.Color("13")
	}
	badge := lipgloss.NewStyle().Bold(true).Foreground(color).Render(mode)
	return badge + "  " + label
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

func (m *conversationModel) cursorColumn() int {
	info := m.input.LineInfo()
	return info.StartColumn + info.ColumnOffset
}

func (m *conversationModel) moveCursorTo(row, col int) {
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	limit := m.input.LineCount() + runeLen(m.input.Value()) + 1
	for m.input.Line() < row && limit > 0 {
		m.input.CursorDown()
		limit--
	}
	m.input.SetCursor(col)
}
