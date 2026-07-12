package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Rows of chrome around the viewport: header, rule, input line, footer.
const conversationChromeRows = 4

// conversationModel renders the live conversation screen: a scrollable
// transcript viewport, a persistent status indicator, and an always-focused
// input line. It renders purely from injected state — turn dispatch and event
// bus wiring are layered on by later slices.
type conversationModel struct {
	agentName string

	width  int
	height int
	ready  bool

	viewport viewport.Model
	input    textinput.Model

	transcript []string
	status     string
	statusErr  bool
}

func newConversation(agentName string) conversationModel {
	if agentName == "" {
		agentName = "Samantha"
	}

	input := textinput.New()
	input.Prompt = "> "
	input.Focus()

	return conversationModel{
		agentName: agentName,
		input:     input,
	}
}

func (m conversationModel) Update(msg tea.Msg) (conversationModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		// PgUp/PgDn (and home/end) scroll the transcript; every other key —
		// including ↑/↓ for cursor movement — belongs to the input line, so
		// typing never needs a mode switch.
		switch msg.String() {
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height

	vpHeight := max(height-conversationChromeRows, 1)
	if !m.ready {
		m.viewport = viewport.New(width, vpHeight)
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = vpHeight
	}
	m.input.Width = max(width-len(m.input.Prompt)-3, 10)
	m.refreshContent()
}

// appendTranscript adds rendered lines to the transcript, following the tail
// only when the user has not scrolled up to review history.
func (m *conversationModel) appendTranscript(lines ...string) {
	follow := !m.ready || m.viewport.AtBottom()
	m.transcript = append(m.transcript, lines...)
	m.refreshContent()
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *conversationModel) clearTranscript() {
	m.transcript = nil
	m.refreshContent()
}

func (m *conversationModel) setStatus(text string, isErr bool) {
	m.status = text
	m.statusErr = isErr
}

func (m *conversationModel) refreshContent() {
	if !m.ready {
		return
	}
	content := strings.Join(m.transcript, "\n")
	// lipgloss wraps to width so long turns don't overflow the viewport.
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

// renderUserTurn and renderAgentTurn are the single rendering path for both
// live events and replayed session history, so the two cannot drift apart.
func renderUserTurn(text string) string {
	return "  " + userStyle.Render("You:") + " " + text
}

func renderAgentTurn(name, text string) string {
	return "  " + samanthaStyle.Render(name+":") + " " + text
}

func (m conversationModel) View() string {
	if !m.ready {
		return "\n  Preparing conversation...\n"
	}

	status := m.status
	style := statusStyle
	if m.statusErr {
		style = errorStyle
	}

	header := "  " + headerStyle.Render(m.agentName)
	if status != "" {
		header += "  " + style.Render(status)
	}

	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))
	footer := dimStyle.Render("  pgup/pgdn scroll • /clear • /voice • ctrl+c quit")

	return header + "\n" + rule + "\n" +
		m.viewport.View() + "\n" +
		"  " + m.input.View() + "\n" +
		footer
}
