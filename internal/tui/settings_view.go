package tui

import (
	"fmt"
	"strings"

	ansi "github.com/charmbracelet/x/ansi"
)

func (m settingsModel) View() string {
	var b strings.Builder
	width := m.renderWidth()
	compact := m.height > 0 && m.height < 12

	b.WriteString(headerStyle.Render("  Settings"))
	b.WriteString("\n")

	tabs := []string{"Brain", "Brain model", "TTS", "Voice", "Input", "Output", "Meeting"}
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
	b.WriteString(ansi.Truncate("  "+tabLine.String(), width, "…"))
	b.WriteString("\n")
	if !compact {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	// Render active section list.
	switch m.section {
	case sectionProvider:
		start, end := m.visibleRange(len(m.providerItems))
		for i := start; i < end; i++ {
			item := m.providerItems[i]
			active := ""
			if i < len(m.providers) && m.providers[i].Name == m.cfg.BrainProvider {
				active = " ✓"
			}
			m.renderItem(&b, i, item+active)
		}

	case sectionModel:
		start, end := m.visibleRange(len(m.modelItems))
		for i := start; i < end; i++ {
			item := m.modelItems[i]
			active := ""
			if item == m.activeModel() {
				active = " ✓"
			}
			m.renderItem(&b, i, item+active)
		}

	case sectionTTS:
		activeProvider := activeTTSProvider(m.cfg)
		for i, item := range m.ttsItems {
			active := ""
			if item.provider == activeProvider {
				active = " ✓"
			}
			m.renderItem(&b, i, item.provider+" — "+item.detail+active)
		}

	case sectionVoice:
		if len(m.voiceItems) == 0 {
			b.WriteString(dimStyle.Render("  No browsable voices for the active TTS provider."))
			b.WriteString("\n")
			break
		}
		start, end := m.visibleRange(len(m.voiceItems))
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
			m.renderItem(&b, i, label)
		}

	case sectionInput:
		m.renderDevices(&b, m.inputItems, m.cfg.InputDevice)

	case sectionOutput:
		m.renderDevices(&b, m.outputItems, m.cfg.OutputDevice)

	case sectionMeeting:
		items := m.meetingItems()
		start, end := m.visibleRange(len(items))
		for i := start; i < end; i++ {
			m.renderItem(&b, i, items[i])
		}
	}

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
		b.WriteString(ansi.Truncate(footer, width, "…"))
	} else {
		if m.message != "" {
			b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		} else {
			b.WriteString(" ")
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	}

	return b.String()
}

func (m *settingsModel) renderItem(b *strings.Builder, idx int, label string) {
	cursor := "  "
	style := normalStyle
	if idx == m.cursor {
		cursor = "▸ "
		style = selectedStyle
	}
	line := "  " + cursor + style.Render(label)
	b.WriteString(ansi.Truncate(line, m.renderWidth(), "…") + "\n")
}

func (m settingsModel) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m *settingsModel) renderDevices(b *strings.Builder, items []string, active string) {
	if m.devicesLoading {
		b.WriteString(dimStyle.Render("  Discovering audio devices..."))
		b.WriteString("\n")
		return
	}
	start, end := m.visibleRange(len(items))
	for i := start; i < end; i++ {
		item := items[i]
		label := deviceLabel(item)
		if item == active {
			label += " ✓"
		}
		m.renderItem(b, i, label)
	}
}
