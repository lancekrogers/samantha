package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

type launcherAction int

const (
	actionContinue launcherAction = iota
	actionNew
	actionSessions
	actionMeeting
	actionRemote
	actionLibrary
	actionAudiobook
	actionPersonas
	actionSettings
	actionQuit
	// Submenu actions (design WI-c8884d §2.4/§2.6).
	actionStartPersona    // start a conversation bound to item.personaID
	actionCreatePersona   // jump to the Personas editor to create one
	actionMeetingStart    // today's meeting setup → record flow
	actionMeetingSettings // Settings opened on the Meeting section
)

type launcherItem struct {
	label     string
	hint      string
	glyph     string
	action    launcherAction
	sessionID string
	personaID string
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

	// Inline submenu (persona picker / meeting split); nil = main menu.
	submenu       []launcherItem
	submenuTitle  string
	submenuCursor int

	// listPersonas feeds the New-conversation picker (injectable for tests).
	listPersonas func() ([]*persona.Profile, error)
}

// withBanner returns a copy carrying a status banner shown above the menu.
func (m launcherModel) withBanner(text string, isErr bool) launcherModel {
	m.banner = strings.TrimSpace(text)
	m.bannerErr = isErr
	return m
}

func newLauncher(cfg *config.Config, providers []discovery.ProviderInfo, saved ...[]session.Session) launcherModel {
	m := launcherModel{
		cfg:          cfg,
		providers:    providers,
		listPersonas: persona.List,
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
	// Active persona hint for the main-menu Personas entry.
	personaHint := "Switch or create voice agents"
	if m.cfg != nil {
		if name := strings.TrimSpace(m.cfg.AgentName); name != "" {
			personaHint = "Active: " + name + " · switch or create"
		}
	}
	m.items = append(m.items,
		launcherItem{
			label: "Personas", hint: personaHint, glyph: "◎",
			action: actionPersonas,
		},
		launcherItem{
			label: "Meeting", hint: "Record · notes · ★ bookmarks", glyph: "◉",
			action: actionMeeting,
		},
		launcherItem{
			label: "Use on another device", hint: "LAN or Tailscale · any client", glyph: "⇄",
			action: actionRemote,
		},
		launcherItem{
			label: "Library", hint: "Optional ebook catalog (Calibre) · browse & audiobooks", glyph: "▤",
			action: actionLibrary,
		},
		launcherItem{
			label: "Create audiobook", hint: "Render long-form narration", glyph: "♪",
			action: actionAudiobook,
		},
		launcherItem{
			label: "Settings", hint: "Brain, TTS, voice, devices", glyph: "⚙",
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
		if m.submenu != nil {
			return m.updateSubmenu(msg)
		}
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
				return m.openPersonaPicker()
			case actionSessions:
				return m, func() tea.Msg { return switchScreenMsg(screenSessions) }
			case actionMeeting:
				return m.openMeetingMenu(), nil
			case actionRemote:
				return m, func() tea.Msg { return switchScreenMsg(screenRemote) }
			case actionLibrary:
				return m, func() tea.Msg { return switchScreenMsg(screenLibrary) }
			case actionAudiobook:
				return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
			case actionPersonas:
				return m, func() tea.Msg { return switchScreenMsg(screenPersonas) }
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

// updateSubmenu drives the inline persona picker / meeting split menu.
func (m launcherModel) updateSubmenu(msg tea.KeyMsg) (launcherModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.submenuCursor > 0 {
			m.submenuCursor--
		}
	case "down", "j":
		if m.submenuCursor < len(m.submenu)-1 {
			m.submenuCursor++
		}
	case "esc", "q":
		m.closeSubmenu()
	case "enter":
		item := m.submenu[m.submenuCursor]
		m.closeSubmenu()
		switch item.action {
		case actionStartPersona:
			return m, func() tea.Msg { return startPipelineMsg{personaID: item.personaID} }
		case actionCreatePersona:
			return m, func() tea.Msg { return switchScreenMsg(screenPersonas) }
		case actionMeetingStart:
			return m, func() tea.Msg { return switchScreenMsg(screenMeetingSetup) }
		case actionMeetingSettings:
			return m, func() tea.Msg { return openSettingsSectionMsg{section: sectionMeeting} }
		}
	}
	return m, nil
}

func (m *launcherModel) closeSubmenu() {
	m.submenu = nil
	m.submenuTitle = ""
	m.submenuCursor = 0
}

// openPersonaPicker builds the New-conversation submenu: every persona (the
// active one pre-selected as the explicit default), plus a create shortcut.
// The conversation never starts without a persona choice; a picker failure
// falls back to the bound default rather than blocking the user.
func (m launcherModel) openPersonaPicker() (launcherModel, tea.Cmd) {
	list := m.listPersonas
	if list == nil {
		list = persona.List
	}
	profiles, err := list()
	if err != nil || len(profiles) == 0 {
		return m, func() tea.Msg { return startPipelineMsg{} }
	}
	active := ""
	if m.cfg != nil {
		active = persona.ActiveID(m.cfg)
	}
	m.submenu = nil
	m.submenuTitle = "New conversation — pick a persona"
	m.submenuCursor = 0
	for _, p := range profiles {
		if p == nil {
			continue
		}
		label := p.DisplayName
		if label == "" {
			label = p.ID
		}
		hint := strings.TrimSpace(strings.TrimSpace(p.TTS.Provider) + " " + strings.TrimSpace(p.TTS.Voice))
		glyph := "·"
		if p.ID == active {
			glyph = "✓"
			if hint != "" {
				hint += " · active"
			} else {
				hint = "active"
			}
			// Cursor tracks the built submenu, not the profiles index — nil
			// holes in the listing must not skew the pre-selection.
			m.submenuCursor = len(m.submenu)
		}
		m.submenu = append(m.submenu, launcherItem{
			label: label, hint: hint, glyph: glyph,
			action: actionStartPersona, personaID: p.ID,
		})
	}
	m.submenu = append(m.submenu, launcherItem{
		label: "+ Create persona…", hint: "Name, prompt, model & voice", glyph: "✦",
		action: actionCreatePersona,
	})
	return m, nil
}

// openMeetingMenu splits Meeting into start vs settings (design §2.6).
func (m launcherModel) openMeetingMenu() launcherModel {
	m.submenuTitle = "Meeting"
	m.submenuCursor = 0
	m.submenu = []launcherItem{
		{label: "Start meeting", hint: "Record · notes · ★ bookmarks", glyph: "◉", action: actionMeetingStart},
		{label: "Meeting settings", hint: "Note routing · destinations", glyph: "⚙", action: actionMeetingSettings},
	}
	return m
}

// visibleItems returns the list and cursor the views should render.
func (m launcherModel) visibleItems() ([]launcherItem, int) {
	if m.submenu != nil {
		return m.submenu, m.submenuCursor
	}
	return m.items, m.cursor
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
	voiceLabel := "voice model-native"
	if activeTTSProvider(m.cfg) == "kokoro" {
		voiceLabel = "voice " + m.cfg.TTSVoice
	}
	chips := lipgloss.JoinHorizontal(lipgloss.Center,
		chipStyle.Render("brain "+brainStatus),
		" ",
		chipMutedStyle.Render("model "+m.activeModel()),
		" ",
		chipMutedStyle.Render(ttsBadgeLabel(m.cfg)),
		" ",
		chipMutedStyle.Render(voiceLabel),
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

	items, cursor := m.visibleItems()
	if m.submenuTitle != "" && m.submenu != nil {
		b.WriteString(headerStyle.Render(ansi.Truncate("  "+m.submenuTitle, width, "…")))
		b.WriteString("\n")
	}
	for i, item := range items {
		g := item.glyph
		if g == "" {
			g = "·"
		}
		label := fmt.Sprintf("%s  %s", g, item.label)
		if i == cursor {
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

	help := "  ↑/↓ navigate   enter select   q quit"
	if m.submenu != nil {
		help = "  ↑/↓ navigate   enter select   esc back"
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
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

	items, cursor := m.visibleItems()
	visible := max(m.height-3, 1)
	start := min(max(cursor-visible/2, 0), max(len(items)-visible, 0))
	end := min(start+visible, len(items))
	for i := start; i < end; i++ {
		item := items[i]
		if i == cursor {
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
