package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/session"
)

type sessionsModel struct {
	sessions []session.Session
	cursor   int
	width    int
	height   int
	offset   int
}

func newSessions(sessions []session.Session) sessionsModel {
	return sessionsModel{sessions: resumableSessions(sessions)}
}

func resumableSessions(sessions []session.Session) []session.Session {
	out := make([]session.Session, 0, len(sessions))
	for _, saved := range sessions {
		if len(saved.Turns) > 0 {
			out = append(out, saved)
		}
	}
	return out
}

func (m sessionsModel) Update(msg tea.Msg) (sessionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(len(m.sessions)-1, 0)
		case "enter":
			if len(m.sessions) > 0 {
				id := m.sessions[m.cursor].ID
				return m, func() tea.Msg { return startPipelineMsg{sessionID: id} }
			}
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		m.ensureVisible()
	}
	return m, nil
}

func (m *sessionsModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(len(m.sessions)-visible, 0)
	m.offset = min(max(m.offset, 0), maxOffset)
}

func (m sessionsModel) visibleRows() int {
	if m.height > 0 && m.height < 10 {
		return max(m.height-2, 1)
	}
	return max(m.height-6, 3)
}

func (m sessionsModel) View() string {
	var b strings.Builder
	width := m.width
	if width <= 0 {
		width = 80
	}
	compact := m.height > 0 && m.height < 10
	title := titleStyle.Render("  Sessions")
	if compact {
		title = headerStyle.Render("  Sessions")
	}
	b.WriteString(ansi.Truncate(title, width, "…"))
	b.WriteString("\n")
	if !compact {
		b.WriteString(ansi.Truncate(subtitleStyle.Render("  Resume a saved conversation"), width, "…"))
		b.WriteString("\n\n")
	}
	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  No saved sessions."))
		b.WriteString("\n")
	} else {
		visible := m.visibleRows()
		end := min(m.offset+visible, len(m.sessions))
		for i := m.offset; i < end; i++ {
			s := m.sessions[i]
			cursor, style := "  ", normalStyle
			if i == m.cursor {
				cursor, style = "▸ ", selectedStyle
			}
			summary := strings.Join(strings.Fields(s.Summary), " ")
			if summary == "" {
				summary = "Untitled conversation"
			}
			age := compactAge(s.UpdatedAt)
			line := fmt.Sprintf("%s  %d turns  %s", summary, len(s.Turns), age)
			maxWidth := max(width-6, 1)
			line = ansi.Truncate(line, maxWidth, "…")
			b.WriteString("  " + cursor + style.Render(line) + "\n")
		}
	}
	if !compact {
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ navigate • enter resume • esc back", width, "…")))
	return b.String()
}

func compactAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
