package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
)

type launcherModel struct {
	cfg       *config.Config
	providers []discovery.ProviderInfo
	cursor    int
	items     []string
}

func newLauncher(cfg *config.Config, providers []discovery.ProviderInfo) launcherModel {
	return launcherModel{
		cfg:       cfg,
		providers: providers,
		items:     []string{"Start conversation", "Create audiobook", "Settings", "Quit"},
	}
}

func (m launcherModel) Update(msg tea.Msg) (launcherModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			switch m.cursor {
			case 0: // Start conversation
				return m, func() tea.Msg { return startPipelineMsg{} }
			case 1: // Create audiobook
				return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
			case 2: // Settings
				return m, func() tea.Msg { return switchScreenMsg(screenSettings) }
			case 3: // Quit
				return m, func() tea.Msg { return quitMsg{} }
			}
		case "q":
			return m, func() tea.Msg { return quitMsg{} }
		}
	}
	return m, nil
}

func (m launcherModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("  Samantha"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("  Ultra-low-latency voice assistant for AI coding"))
	b.WriteString("\n\n")

	// Current config summary.
	brainStatus := m.cfg.BrainProvider
	for _, p := range m.providers {
		if p.Name == m.cfg.BrainProvider {
			if !p.Available {
				brainStatus += " " + errorStyle.Render("(not available)")
			}
		}
	}

	model := m.activeModel()

	b.WriteString(dimStyle.Render(fmt.Sprintf("  Brain: %s  Model: %s  Voice: %s", brainStatus, model, m.cfg.TTSVoice)))
	b.WriteString("\n\n")

	// Menu items.
	for i, item := range m.items {
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		b.WriteString("  " + cursor + style.Render(item) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑/↓ navigate • enter select • q quit"))
	b.WriteString("\n")

	return b.String()
}

func (m launcherModel) activeModel() string {
	switch m.cfg.BrainProvider {
	case "ollama":
		if m.cfg.OllamaModel != "" {
			return m.cfg.OllamaModel
		}
	case "grok":
		if m.cfg.GrokModel != "" {
			return m.cfg.GrokModel
		}
	}
	return "default"
}
