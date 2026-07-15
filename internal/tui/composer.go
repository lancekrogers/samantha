package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

type vimInputMode int

const (
	vimInsert vimInputMode = iota
	vimNormal
)

type composerSnapshot struct {
	value string
	row   int
	col   int
}

func (m *conversationModel) updateComposer(msg tea.KeyMsg) tea.Cmd {
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
	if m.cursorColumn() > 0 {
		m.updateComposer(tea.KeyMsg{Type: tea.KeyLeft})
	}
}

func (m *conversationModel) handleVimNormalKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.vimPending == "d" && key != "d" {
		m.vimPending = ""
	}

	switch key {
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
	case "h", "left":
		if m.cursorColumn() > 0 {
			return m.updateComposer(tea.KeyMsg{Type: tea.KeyLeft})
		}
	case "l", "right":
		if m.cursorColumn() < m.currentLineLength()-1 {
			return m.updateComposer(tea.KeyMsg{Type: tea.KeyRight})
		}
	case "j", "down":
		m.input.CursorDown()
	case "k", "up":
		m.input.CursorUp()
	case "0", "home":
		m.input.CursorStart()
	case "$", "end":
		m.input.CursorEnd()
		if m.currentLineLength() > 0 {
			return m.updateComposer(tea.KeyMsg{Type: tea.KeyLeft})
		}
	case "w":
		return m.updateComposer(tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	case "b":
		return m.updateComposer(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	case "x", "delete":
		m.pushVimUndo()
		return m.updateComposer(tea.KeyMsg{Type: tea.KeyDelete})
	case "D":
		m.pushVimUndo()
		return m.updateComposer(tea.KeyMsg{Type: tea.KeyCtrlK})
	case "C":
		m.pushVimUndo()
		m.vimMode = vimInsert
		return m.updateComposer(tea.KeyMsg{Type: tea.KeyCtrlK})
	case "d":
		if m.vimPending == "d" {
			m.vimPending = ""
			m.pushVimUndo()
			m.deleteVimLine()
		} else {
			m.vimPending = "d"
		}
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
	case "esc":
		m.vimPending = ""
	}
	return nil
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
	pending := ""
	if m.vimPending != "" {
		pending = "  pending " + m.vimPending
	}
	return "NORMAL" + pending + "  i/a insert  hjkl move  w/b word  x/dd delete  u undo  enter send"
}

func (m conversationModel) vimCompactFooterHelp() string {
	if m.vimMode == vimInsert {
		return "INSERT  esc normal  enter send  tab complete"
	}
	pending := ""
	if m.vimPending != "" {
		pending = " " + m.vimPending
	}
	return "NORMAL" + pending + "  i/a insert  hjkl move  x/dd delete  u undo"
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
	}
	badge := lipgloss.NewStyle().Bold(true).Foreground(color).Render(mode)
	return badge + "  " + label
}
