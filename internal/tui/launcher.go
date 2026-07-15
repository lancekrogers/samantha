package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/session"
)

type launcherAction int

const (
	actionContinue launcherAction = iota
	actionNew
	actionSessions
	actionTailscale
	actionAudiobook
	actionSettings
	actionQuit
)

type launcherItem struct {
	label     string
	action    launcherAction
	sessionID string
}

type launcherModel struct {
	cfg       *config.Config
	providers []discovery.ProviderInfo
	cursor    int
	items     []launcherItem
	width     int
	height    int
}

func newLauncher(cfg *config.Config, providers []discovery.ProviderInfo, saved ...[]session.Session) launcherModel {
	m := launcherModel{
		cfg:       cfg,
		providers: providers,
	}
	var sessions []session.Session
	if len(saved) > 0 {
		sessions = saved[0]
	}
	if len(sessions) > 0 {
		summary := strings.Join(strings.Fields(sessions[0].Summary), " ")
		label := "Continue: " + summary
		if summary == "" {
			label = "Continue recent conversation"
		}
		m.items = append(m.items, launcherItem{label: label, action: actionContinue, sessionID: sessions[0].ID})
	}
	m.items = append(m.items, launcherItem{label: "New conversation", action: actionNew})
	if len(sessions) > 0 {
		m.items = append(m.items, launcherItem{label: "Browse sessions", action: actionSessions})
	}
	m.items = append(m.items,
		launcherItem{label: "Use on iPad (Tailscale)", action: actionTailscale},
		launcherItem{label: "Create audiobook", action: actionAudiobook},
		launcherItem{label: "Settings", action: actionSettings},
		launcherItem{label: "Quit", action: actionQuit},
	)
	return m
}

func (m launcherModel) Update(msg tea.Msg) (launcherModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

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
			item := m.items[m.cursor]
			switch item.action {
			case actionContinue:
				return m, func() tea.Msg { return startPipelineMsg{sessionID: item.sessionID} }
			case actionNew:
				return m, func() tea.Msg { return startPipelineMsg{} }
			case actionSessions:
				return m, func() tea.Msg { return switchScreenMsg(screenSessions) }
			case actionTailscale:
				return m, func() tea.Msg { return switchScreenMsg(screenTailscale) }
			case actionAudiobook:
				return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
			case actionSettings:
				return m, func() tea.Msg { return switchScreenMsg(screenSettings) }
			case actionQuit:
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
	width := m.width
	if width <= 0 {
		width = 80
	}
	compact := m.height > 0 && m.height < 14

	title := titleStyle.Render("  Samantha")
	if compact {
		title = headerStyle.Render("  Samantha")
	}
	b.WriteString(ansi.Truncate(title, width, "…"))
	b.WriteString("\n")
	if !compact {
		b.WriteString(ansi.Truncate(subtitleStyle.Render("  Ultra-low-latency voice assistant for AI coding"), width, "…"))
		b.WriteString("\n\n")
	}

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

	if !compact {
		b.WriteString(ansi.Truncate(dimStyle.Render(fmt.Sprintf("  Brain: %s  Model: %s  Voice: %s", brainStatus, model, m.cfg.TTSVoice)), width, "…"))
		b.WriteString("\n\n")
	}

	// Menu items.
	start, end := 0, len(m.items)
	if compact {
		visible := max(m.height-3, 1)
		start = min(max(m.cursor-visible/2, 0), max(len(m.items)-visible, 0))
		end = min(start+visible, len(m.items))
	}
	for i := start; i < end; i++ {
		item := m.items[i]
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		b.WriteString(ansi.Truncate("  "+cursor+style.Render(item.label), width, "…") + "\n")
	}

	if !compact {
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ navigate • enter select • q quit", width, "…")))
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
