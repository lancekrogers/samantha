package tui

import (
	"fmt"
	"strings"

	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/persona"
)

func (m personasModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("  Personas"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  Switch, create, or edit voice agents (system prompt included)", width, "…")))
	b.WriteString("\n")
	if m.height == 0 || m.height >= 10 {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	listRows := m.visibleRows()
	if m.formMode != "" {
		for _, line := range m.formLines(listRows) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	} else {
		for _, line := range m.listLines(listRows) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if m.message != "" {
		b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
	}
	help := "  ↑/↓ navigate • enter switch/create • e edit • esc back"
	if m.formMode != "" {
		help = "  tab fields • ctrl+s save & activate • enter next field (name) / newline (prompt) • esc cancel"
	}
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	return b.String()
}

func (m personasModel) listLines(listRows int) []string {
	if m.loadErr != "" {
		return padPersonasLines([]string{"  error loading personas: " + m.loadErr}, listRows)
	}
	active := ""
	if m.cfg != nil {
		active = persona.ActiveID(m.cfg)
	}
	total := m.listLen()
	start := m.offset
	end := min(start+listRows, total)
	lines := make([]string, 0, listRows)
	for i := start; i < end; i++ {
		if i == len(m.items) {
			lines = append(lines, m.row(i, personasCreateLabel))
			continue
		}
		p := m.items[i]
		mark := ""
		if p != nil && p.ID == active {
			mark = " ✓"
		}
		lines = append(lines, m.row(i, personaListLabel(p)+mark))
	}
	return padPersonasLines(lines, listRows)
}

func (m personasModel) formLines(listRows int) []string {
	title := "  Create a new voice agent"
	if m.formMode == "edit" {
		title = "  Edit persona " + m.editID
	}
	slug := persona.Slugify(m.nameInput.Value())
	if slug == "" {
		slug = "persona"
	}
	nameMark, promptMark := " ", " "
	if m.formStep == personaFormName {
		nameMark = "▸"
	} else {
		promptMark = "▸"
	}
	lines := []string{
		title,
		"",
		fmt.Sprintf("%s Name", nameMark),
		m.nameInput.View(),
	}
	if m.formMode == "create" {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  id will be: %s", slug)))
	}
	lines = append(lines,
		"",
		fmt.Sprintf("%s System prompt  (supports {agent_name})", promptMark),
		m.promptTA.View(),
		"",
		dimStyle.Render("  ctrl+s save · tab switch fields · esc cancel"),
	)
	return padPersonasLines(lines, listRows)
}

func (m personasModel) row(i int, label string) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	prefix := "  "
	style := dimStyle
	if i == m.cursor {
		prefix = "▸ "
		style = selectedStyle
	}
	return style.Render(ansi.Truncate(prefix+label, width, "…"))
}

func padPersonasLines(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}
