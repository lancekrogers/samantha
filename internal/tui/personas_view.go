package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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
	b.WriteString(dimStyle.Render(ansi.Truncate("  Switch agents · create/edit system prompts the brain actually loads", width, "…")))
	b.WriteString("\n")
	if m.height == 0 || m.height >= 10 {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	if m.formMode != "" {
		// Form is never pad-truncated: the prompt field was easy to clip when we
		// reused the list body height budget.
		for _, line := range m.formLines() {
			b.WriteString(ansi.Truncate(line, width, "…"))
			b.WriteString("\n")
		}
	} else {
		listRows := m.visibleRows()
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
	help := "  ↑/↓ navigate • enter switch • n create • e edit • esc back"
	if m.formMode != "" {
		help = "  tab fields • save: ctrl+j · alt+s · f2 • esc cancel"
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

// formBoxWidth is the outer width of the form's field boxes, leaving a
// 2-column indent inside the terminal.
func (m personasModel) formBoxWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return max(min(w-2, 78), 24)
}

// personaStepPills renders the wizard progress line: done, current, upcoming.
func personaStepPills(current int) string {
	steps := []string{"Name", "Prompt", "Model & voice"}
	parts := make([]string, 0, len(steps))
	for i, s := range steps {
		switch {
		case i < current:
			parts = append(parts, statusStyle.Render("✓ "+s))
		case i == current:
			parts = append(parts, selectedStyle.Render("▸ "+s))
		default:
			parts = append(parts, dimStyle.Render("· "+s))
		}
	}
	return "  " + strings.Join(parts, "   ")
}

// formBox draws a titled, rounded box around body lines. The title is already
// styled; the border tracks focus (accent when active, dim otherwise).
func formBox(title string, body []string, width int, focused bool) []string {
	border := dimStyle
	if focused {
		border = lipgloss.NewStyle().Foreground(colorAccent)
	}
	inner := width - 4
	titleText := " " + title + " "
	fill := max(width-3-ansi.StringWidth(titleText), 0)
	lines := make([]string, 0, len(body)+2)
	lines = append(lines, "  "+border.Render("╭─")+titleText+border.Render(strings.Repeat("─", fill)+"╮"))
	for _, line := range body {
		content := ansi.Truncate(line, inner, "…")
		pad := max(inner-ansi.StringWidth(content), 0)
		lines = append(lines, "  "+border.Render("│")+" "+content+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	lines = append(lines, "  "+border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return lines
}

func (m personasModel) formLines() []string {
	title := "Create a new voice agent"
	if m.formMode == "edit" {
		title = "Edit persona " + m.editID
	}
	boxW := m.formBoxWidth()

	nameTitle := headerStyle.Render("Name")
	if m.formMode == "create" {
		slug := persona.Slugify(m.nameInput.Value())
		if slug == "" {
			slug = "persona"
		}
		nameTitle += dimStyle.Render(" · id: " + slug)
	}
	promptTitle := headerStyle.Render("System prompt") + dimStyle.Render(" · {agent_name} supported")
	if m.formStep == personaFormPrompt {
		promptTitle += dimStyle.Render(" — ") + m.promptTA.modeChip()
	}
	stackTitle := headerStyle.Render("Model & voice") + dimStyle.Render(" · (default) inherits Settings")

	lines := []string{
		"  " + normalStyle.Bold(true).Render(title),
		personaStepPills(m.formStep),
		"",
	}
	lines = append(lines, formBox(nameTitle, []string{m.nameInput.View()}, boxW, m.formStep == personaFormName)...)
	lines = append(lines, formBox(promptTitle, strings.Split(m.promptTA.View(), "\n"), boxW, m.formStep == personaFormPrompt)...)
	if m.formStep == personaFormPrompt {
		lines = append(lines, dimStyle.Render("  "+m.promptTA.modeline()))
	}
	lines = append(lines, formBox(stackTitle, m.stackLines(), boxW, m.formStep == personaFormStack)...)
	return lines
}

// stackLines renders the model/voice rows of the form's stack step.
func (m personasModel) stackLines() []string {
	row := func(idx int, label, value string) string {
		mark, labelStyle := "  ", dimStyle
		if m.formStep == personaFormStack && m.stackRow == idx {
			mark, labelStyle = selectedStyle.Render("▸ "), selectedStyle
		}
		return mark + labelStyle.Render(label) + " " + value
	}
	brainProvider := stackBrainProviders()[m.brainProviderIdx]
	brainRow := row(stackRowBrainProvider, "Brain", "‹ "+brainProvider+" ›")
	if strings.EqualFold(brainProvider, "claude") && strings.TrimSpace(m.brainModelInput.Value()) != "" {
		// Claude has no app-level model key yet; be honest that the model
		// string is recorded on the profile but not routed.
		brainRow += dimStyle.Render("  (model saved, not routed for claude yet)")
	}
	ttsProvider := stackTTSProviders()[m.ttsProviderIdx]
	voices := m.stackVoiceList()
	voiceLabel := stackDefaultLabel
	if idx := m.stackVoiceIndex(); idx >= 0 && idx < len(voices) {
		voiceLabel = voices[idx]
	}
	tierValue := dimStyle.Render("— (qwen3-tts only)")
	if m.stackTierUsable() {
		tiers := m.stackTierList()
		tierLabel := stackDefaultLabel
		if idx := m.stackTierIndex(); idx >= 0 && idx < len(tiers) {
			tierLabel = tiers[idx]
		}
		tierValue = "‹ " + tierLabel + " ›"
	}
	return []string{
		brainRow,
		row(stackRowBrainModel, "Model", m.brainModelInput.View()),
		row(stackRowTTSProvider, "TTS  ", "‹ "+ttsProvider+" ›"),
		row(stackRowVoice, "Voice", "‹ "+voiceLabel+" ›"),
		row(stackRowTier, "Tier ", tierValue),
	}
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
