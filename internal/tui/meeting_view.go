package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/tui/anim"
)

func (m *meetingModel) ensureVoiceTick() tea.Cmd {
	if m.reducedMotion || m.voiceMode == anim.ModeIdle || m.voiceTicking || m.quitting {
		return nil
	}
	m.voiceTicking = true
	return meetingTickCmd()
}

func (m *meetingModel) reflow() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// header×2 + rules + stage + partial + note box + footer
	chrome := 14 + meetingNoteHeight
	vpH := max(m.height-chrome, 3)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpH)
		m.ready = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpH
	}
	m.note.SetWidth(max(m.width-4, 10))
	m.note.SetHeight(meetingNoteHeight)
	m.refreshTranscript()
}

func (m *meetingModel) appendLine(line string) {
	follow := !m.ready || m.viewport.AtBottom()
	m.lines = append(m.lines, line)
	if len(m.lines) > meetingMaxLines {
		m.lines = m.lines[len(m.lines)-meetingMaxLines:]
	}
	m.refreshTranscript()
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *meetingModel) refreshTranscript() {
	if !m.ready {
		return
	}
	content := strings.Join(m.lines, "\n")
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

func (m meetingModel) View() string {
	if !m.ready {
		return "\n  " + headerStyle.Render("Starting meeting recorder…") + "\n"
	}
	w := max(m.width, 1)
	styles := voiceAnimStyles()

	rec := errorStyle.Bold(true).Render("● REC")
	elapsed := formatMeetingDuration(time.Since(m.started).Round(time.Second))
	header := fmt.Sprintf("%s  %s  %s  %s",
		headerStyle.Render("Meeting"),
		normalStyle.Render(m.opts.Description),
		rec,
		dimStyle.Render(elapsed),
	)
	header = ansi.Truncate(header, w, "…")

	paths := m.opts.Path
	if m.opts.Writer != nil {
		paths = m.opts.Writer.Path() + "  +  " + m.opts.Writer.JSONLPath()
	}
	pathLine := dimStyle.Render(ansi.Truncate("  "+paths, w, "…"))
	rule := lipgloss.NewStyle().Foreground(m.meterBorderColor()).Render(strings.Repeat("─", w))

	stage := anim.Stage(m.voiceMode, m.voiceFrame, m.inputLevel, w, m.status, styles, m.reducedMotion)
	if stage != "" {
		stage += "\n"
	}

	partial := ""
	if m.partial != "" {
		partial = dimStyle.Render("  … ") + hearingStyle.Render(ansi.Truncate(m.partial, max(w-4, 1), "…")) + "\n"
	}

	// Action bar (menu of available commands).
	actions := []string{
		chipStyle.Render("Enter note"),
		lipgloss.NewStyle().Foreground(colorBg).Background(colorSpeak).Bold(true).Padding(0, 1).Render("Ctrl+B important"),
		chipMutedStyle.Render("Ctrl+C stop"),
	}
	actionBar := "  " + strings.Join(actions, "  ")
	if m.flash != "" {
		actionBar += "  " + statusStyle.Render(m.flash)
	}
	actionBar = ansi.Truncate(actionBar, w, "…")

	noteBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorHearing).
		Padding(0, 1).
		Render(m.note.View())

	footer := fmt.Sprintf("  %d spoken  ·  %d notes  ·  %d ★  ·  say \"stop recording\"",
		m.utterances, m.notes, m.bookmarks)
	if m.errors > 0 {
		footer += fmt.Sprintf("  ·  %d errors", m.errors)
	}
	footer = dimStyle.Render(ansi.Truncate(footer, w, "…"))

	return header + "\n" + pathLine + "\n" + rule + "\n" +
		stage + partial +
		m.viewport.View() + "\n" +
		rule + "\n" +
		actionBar + "\n" +
		noteBox + "\n" +
		footer
}

func (m meetingModel) meterBorderColor() lipgloss.Color {
	switch m.voiceMode {
	case anim.ModeHearing:
		return colorHearing
	case anim.ModeListening:
		return colorAccent
	case anim.ModeTranscribing:
		return colorStatus
	case anim.ModeError:
		return colorError
	default:
		return colorDim
	}
}

func formatMeetingDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	mi := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mi, s)
	}
	return fmt.Sprintf("%02d:%02d", mi, s)
}
