package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	actionMeeting
	actionRemote
	actionAudiobook
	actionSettings
	actionQuit
)

type launcherItem struct {
	label     string
	hint      string
	glyph     string
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
	// banner is a one-shot status line (e.g. meeting close error after return).
	banner    string
	bannerErr bool
}

// withBanner returns a copy carrying a status banner shown above the menu.
func (m launcherModel) withBanner(text string, isErr bool) launcherModel {
	m.banner = strings.TrimSpace(text)
	m.bannerErr = isErr
	return m
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
		label := "Continue"
		hint := summary
		if summary == "" {
			hint = "Resume the latest session"
		}
		// Keep resume summary visible in the primary label when present.
		if summary != "" {
			label = "Continue: " + summary
			hint = "Resume this session"
		}
		m.items = append(m.items, launcherItem{
			label: label, hint: hint, glyph: "↻",
			action: actionContinue, sessionID: sessions[0].ID,
		})
	}
	m.items = append(m.items, launcherItem{
		label: "New conversation", hint: "Voice + tools, fresh session", glyph: "✦",
		action: actionNew,
	})
	if len(sessions) > 0 {
		m.items = append(m.items, launcherItem{
			label: "Browse sessions", hint: "Pick a past conversation", glyph: "☰",
			action: actionSessions,
		})
	}
	m.items = append(m.items,
		launcherItem{
			label: "Meeting", hint: "Record · notes · ★ bookmarks", glyph: "◉",
			action: actionMeeting,
		},
		launcherItem{
			label: "Use on another device", hint: "LAN or Tailscale · any client", glyph: "⇄",
			action: actionRemote,
		},
		launcherItem{
			label: "Create audiobook", hint: "Render long-form narration", glyph: "♪",
			action: actionAudiobook,
		},
		launcherItem{
			label: "Settings", hint: "Brain, voice, devices", glyph: "⚙",
			action: actionSettings,
		},
		launcherItem{
			label: "Quit", hint: "Exit Samantha", glyph: "✕",
			action: actionQuit,
		},
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
			case actionMeeting:
				return m, func() tea.Msg { return switchScreenMsg(screenMeetingSetup) }
			case actionRemote:
				return m, func() tea.Msg { return switchScreenMsg(screenRemote) }
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
	width := m.width
	if width <= 0 {
		width = 80
	}
	if m.height > 0 && m.height < 16 {
		return m.compactView(width)
	}
	return m.fullView(width)
}

func (m launcherModel) fullView(width int) string {
	var b strings.Builder

	// Brand plate
	wordmark := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("SAMANTHA")
	tag := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("voice · speed · signal")
	brand := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, wordmark, tag))
	b.WriteString(brand)
	b.WriteString("\n\n")

	// Status chips
	brainStatus := m.cfg.BrainProvider
	for _, p := range m.providers {
		if p.Name == m.cfg.BrainProvider && !p.Available {
			brainStatus += " !"
		}
	}
	chips := lipgloss.JoinHorizontal(lipgloss.Center,
		chipStyle.Render("brain "+brainStatus),
		" ",
		chipMutedStyle.Render("model "+m.activeModel()),
		" ",
		chipMutedStyle.Render("voice "+m.cfg.TTSVoice),
	)
	b.WriteString(ansi.Truncate(chips, width, "…"))
	b.WriteString("\n\n")

	if m.banner != "" {
		style := statusStyle
		if m.bannerErr {
			style = errorStyle
		}
		b.WriteString(ansi.Truncate(style.Render("  "+m.banner), width, "…"))
		b.WriteString("\n\n")
	}

	// Menu width
	menuWidth := width - 2
	if menuWidth > 58 {
		menuWidth = 58
	}
	if menuWidth < 24 {
		menuWidth = max(width-1, 16)
	}

	sel := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBg).
		Background(colorAccent).
		Width(menuWidth).
		Padding(0, 1)
	idle := lipgloss.NewStyle().
		Foreground(colorNormal).
		Width(menuWidth).
		Padding(0, 1)
	hint := lipgloss.NewStyle().
		Foreground(colorDim).
		Width(menuWidth).
		PaddingLeft(4)

	for i, item := range m.items {
		g := item.glyph
		if g == "" {
			g = "·"
		}
		label := fmt.Sprintf("%s  %s", g, item.label)
		if i == m.cursor {
			b.WriteString(sel.Render(ansi.Truncate(label, menuWidth-2, "…")))
			b.WriteString("\n")
			if item.hint != "" {
				b.WriteString(hint.Render(ansi.Truncate(item.hint, menuWidth-4, "…")))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(idle.Render(ansi.Truncate(label, menuWidth-2, "…")))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ navigate   enter select   q quit", width, "…")))
	b.WriteString("\n")
	return b.String()
}

func (m launcherModel) compactView(width int) string {
	var b strings.Builder
	b.WriteString(ansi.Truncate(headerStyle.Render("  SAMANTHA"), width, "…"))
	b.WriteString("\n")
	if m.banner != "" {
		style := statusStyle
		if m.bannerErr {
			style = errorStyle
		}
		b.WriteString(ansi.Truncate(style.Render("  "+m.banner), width, "…"))
		b.WriteString("\n")
	}

	visible := max(m.height-3, 1)
	start := min(max(m.cursor-visible/2, 0), max(len(m.items)-visible, 0))
	end := min(start+visible, len(m.items))
	for i := start; i < end; i++ {
		item := m.items[i]
		if i == m.cursor {
			line := lipgloss.NewStyle().
				Bold(true).
				Foreground(colorBg).
				Background(colorAccent).
				Render(" ▸ " + item.label + " ")
			b.WriteString(ansi.Truncate(line, width, "…") + "\n")
		} else {
			b.WriteString(ansi.Truncate(dimStyle.Render("   "+item.label), width, "…") + "\n")
		}
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ · enter · q", width, "…")))
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
