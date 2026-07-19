package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

// meetingSetupModel is the launcher sub-screen that collects a meeting title
// before the recorder starts — same pattern as other main-menu flows.
type meetingSetupModel struct {
	input  textinput.Model
	width  int
	height int
	err    string
}

// startMeetingMsg asks the App to build STT resources and open the recorder.
type startMeetingMsg struct {
	Description string
}

// meetingDoneMsg returns from the embedded recorder to the launcher.
type meetingDoneMsg struct {
	Err error
}

func newMeetingSetup() meetingSetupModel {
	ti := textinput.New()
	ti.Placeholder = "Weekly planning sync"
	ti.CharLimit = 120
	ti.Width = 48
	ti.Focus()
	return meetingSetupModel{input: ti}
}

func (m meetingSetupModel) Update(msg tea.Msg) (meetingSetupModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(m.width-8, 20)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "enter":
			desc := strings.TrimSpace(m.input.Value())
			if desc == "" {
				desc = "meeting"
			}
			return m, func() tea.Msg { return startMeetingMsg{Description: desc} }
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m meetingSetupModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	b.WriteString(ansi.Truncate(titleStyle.Render("  Record meeting"), w, "…"))
	b.WriteString("\n")
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  STT only — notes, ★ bookmarks, voice EQ"), w, "…"))
	b.WriteString("\n\n")
	b.WriteString(ansi.Truncate(dimStyle.Render("  Meeting title"), w, "…"))
	b.WriteString("\n  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	if m.err != "" {
		b.WriteString(ansi.Truncate(errorStyle.Render("  "+m.err), w, "…"))
		b.WriteString("\n\n")
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  enter start recording  •  esc back", w, "…")))
	b.WriteString("\n")
	return b.String()
}
