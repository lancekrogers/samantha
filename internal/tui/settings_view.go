package tui

import (
	"fmt"
	"strings"

	ansi "github.com/charmbracelet/x/ansi"
)

func (m settingsModel) View() string {
	width := m.renderWidth()
	compact := m.isCompact()
	listRows := m.visibleRows()

	var parts []string
	parts = append(parts, headerStyle.Render("  Settings"))

	tabs := []string{"Brain", "Brain model", "Tools", "TTS", "Voice", "Input", "Output", "Meeting"}
	var tabLine strings.Builder
	for i, tab := range tabs {
		style := dimStyle
		if settingsSection(i) == m.section {
			style = selectedStyle
		}
		if i > 0 {
			tabLine.WriteString("  ")
		}
		tabLine.WriteString(style.Render(tab))
	}
	parts = append(parts, ansi.Truncate("  "+tabLine.String(), width, "…"))
	if !compact {
		// Full terminal width so the chrome tracks resizes/splits.
		parts = append(parts, dimStyle.Render(strings.Repeat("─", max(width, 1))))
	}

	listLines := m.sectionListLines()
	// Fill the list region so short sections still expand with the terminal.
	if len(listLines) > listRows {
		listLines = listLines[:listRows]
	}
	for len(listLines) < listRows {
		listLines = append(listLines, "")
	}
	parts = append(parts, listLines...)

	help := "  ←/→ section • ↑/↓ navigate • enter select"
	if m.section == sectionVoice {
		help += " • p preview"
	}
	help += " • esc back"
	if compact {
		footer := dimStyle.Render(help)
		if m.message != "" {
			footer = statusStyle.Render("  " + m.message)
		}
		parts = append(parts, ansi.Truncate(footer, width, "…"))
	} else {
		if m.message != "" {
			parts = append(parts, ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		} else {
			parts = append(parts, " ")
		}
		parts = append(parts, dimStyle.Render(ansi.Truncate(help, width, "…")))
	}

	return strings.Join(parts, "\n")
}

func (m settingsModel) isCompact() bool {
	return m.height > 0 && m.height < 12
}

func (m settingsModel) sectionListLines() []string {
	switch m.section {
	case sectionProvider:
		start, end := m.visibleRange(len(m.providerItems))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			item := m.providerItems[i]
			active := ""
			if i < len(m.providers) && m.providers[i].Name == m.cfg.BrainProvider {
				active = " ✓"
			}
			lines = append(lines, m.itemLine(i, item+active))
		}
		return lines

	case sectionModel:
		start, end := m.visibleRange(len(m.modelItems))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			item := m.modelItems[i]
			active := ""
			if item == m.activeModel() {
				active = " ✓"
			}
			lines = append(lines, m.itemLine(i, item+active))
		}
		return lines

	case sectionTools:
		start, end := m.visibleRange(len(m.toolItems))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			lines = append(lines, m.itemLine(i, m.toolItems[i]))
		}
		return lines

	case sectionTTS:
		activeProvider := activeTTSProvider(m.cfg)
		start, end := m.visibleRange(len(m.ttsItems))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			item := m.ttsItems[i]
			active := ""
			if item.provider == activeProvider {
				active = " ✓"
			}
			lines = append(lines, m.itemLine(i, item.provider+" — "+item.detail+active))
		}
		return lines

	case sectionVoice:
		if len(m.voiceItems) == 0 {
			return []string{dimStyle.Render("  " + ttsVoiceSelectionStatus(m.cfg))}
		}
		start, end := m.visibleRange(len(m.voiceItems))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			v := m.voiceItems[i]
			active := ""
			if v.Name == m.cfg.TTSVoice {
				active = " ✓"
			}
			preview := ""
			if v.Name == m.previewing {
				preview = " ♫ playing..."
			}
			label := fmt.Sprintf("%-16s %s / %s%s%s", v.Name, v.Gender, v.Locale, active, preview)
			lines = append(lines, m.itemLine(i, label))
		}
		return lines

	case sectionInput:
		return m.deviceLines(m.inputItems, m.cfg.InputDevice)

	case sectionOutput:
		return m.deviceLines(m.outputItems, m.cfg.OutputDevice)

	case sectionMeeting:
		items := m.meetingItems()
		start, end := m.visibleRange(len(items))
		lines := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			lines = append(lines, m.itemLine(i, items[i]))
		}
		return lines
	}
	return nil
}

func (m settingsModel) itemLine(idx int, label string) string {
	cursor := "  "
	style := normalStyle
	if idx == m.cursor {
		cursor = "▸ "
		style = selectedStyle
	}
	line := "  " + cursor + style.Render(label)
	return ansi.Truncate(line, m.renderWidth(), "…")
}

func (m settingsModel) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m settingsModel) deviceLines(items []string, active string) []string {
	if m.devicesLoading {
		return []string{dimStyle.Render("  Discovering audio devices...")}
	}
	start, end := m.visibleRange(len(items))
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := items[i]
		label := deviceLabel(item)
		if item == active {
			label += " ✓"
		}
		lines = append(lines, m.itemLine(i, label))
	}
	return lines
}
