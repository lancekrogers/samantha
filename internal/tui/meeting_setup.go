package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
)

// Setup steps: title first (camp idea-add style), then quick route pick.
const (
	meetingSetupTitle = iota
	meetingSetupRoute
)

// Per-session route plan chosen at meeting start.
const (
	routePlanLocal = "local" // keep notes local; skip post-meeting picker
	routePlanAsk   = "ask"   // ask at end (discover destinations again)
	routePlanDest  = "dest"  // route to a specific destination at end
)

// meetingSetupModel is the launcher sub-screen: title → route destination.
type meetingSetupModel struct {
	cfg    *config.Config
	step   int
	input  textinput.Model
	width  int
	height int
	err    string

	// Route step.
	dests   []meeting.Destination
	cursor  int
	loading bool
	// loadSeq ignores stale async discovery results.
	loadSeq int
}

// startMeetingMsg asks the App to build STT resources and open the recorder.
type startMeetingMsg struct {
	Description string
	// RoutePlan is local | ask | dest.
	RoutePlan string
	// Destination is set when RoutePlan is dest (may be ephemeral from camp list).
	Destination meeting.Destination
}

// meetingDoneMsg returns from the embedded recorder to the launcher / route picker.
type meetingDoneMsg struct {
	Err error
}

// meetingDestsMsg folds async DiscoverDestinations into the setup model.
type meetingDestsMsg struct {
	seq   int
	dests []meeting.Destination
	err   error
}

func newMeetingSetup(cfg *config.Config) meetingSetupModel {
	ti := textinput.New()
	ti.Placeholder = "Weekly planning sync"
	ti.CharLimit = 120
	ti.Width = 48
	ti.Focus()
	return meetingSetupModel{
		cfg:   cfg,
		step:  meetingSetupTitle,
		input: ti,
	}
}

func (m meetingSetupModel) loadDestinations() tea.Cmd {
	m.loading = true
	seq := m.loadSeq
	cfg := m.cfg
	return func() tea.Msg {
		routeCfg := meeting.FromConfig(cfg)
		router := meeting.NewDefaultRouter(routeCfg)
		ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
		defer cancel()
		dests := router.DiscoverDestinations(ctx)
		return meetingDestsMsg{seq: seq, dests: dests}
	}
}

func (m meetingSetupModel) Update(msg tea.Msg) (meetingSetupModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(m.width-8, 20)

	case meetingDestsMsg:
		if msg.seq != m.loadSeq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			m.dests = nil
			return m, nil
		}
		m.err = ""
		m.dests = msg.dests
		// Prefer configured default when present (cursor 2+ maps to dests).
		m.cursor = 1 // default "ask when ends"
		if m.cfg != nil {
			def := strings.TrimSpace(m.cfg.Meeting.Route.Default)
			if def != "" {
				for i, d := range m.dests {
					if d.ID == def {
						m.cursor = i + 2
						break
					}
				}
			}
		}

	case tea.KeyMsg:
		key := msg.String()
		if m.step == meetingSetupRoute {
			return m.updateRouteKey(key)
		}
		switch key {
		case "esc":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "enter":
			desc := strings.TrimSpace(m.input.Value())
			if desc == "" {
				desc = "meeting"
			}
			// Advance to route step; kick discovery.
			m.step = meetingSetupRoute
			m.err = ""
			m.loading = true
			m.loadSeq++
			m.cursor = 1
			cmd := m.loadDestinations()
			return m, cmd
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m meetingSetupModel) updateRouteKey(key string) (meetingSetupModel, tea.Cmd) {
	if m.loading {
		if key == "esc" {
			m.step = meetingSetupTitle
			m.loading = false
			m.err = ""
			return m, nil
		}
		return m, nil
	}
	listLen := 2 + len(m.dests) // keep local, ask, …dests
	switch key {
	case "esc":
		m.step = meetingSetupTitle
		m.err = ""
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < listLen-1 {
			m.cursor++
		}
	case "enter":
		desc := strings.TrimSpace(m.input.Value())
		if desc == "" {
			desc = "meeting"
		}
		msg := startMeetingMsg{Description: desc, RoutePlan: routePlanAsk}
		switch {
		case m.cursor == 0:
			msg.RoutePlan = routePlanLocal
		case m.cursor == 1:
			msg.RoutePlan = routePlanAsk
		default:
			idx := m.cursor - 2
			if idx >= 0 && idx < len(m.dests) {
				msg.RoutePlan = routePlanDest
				msg.Destination = m.dests[idx]
			}
		}
		return m, func() tea.Msg { return msg }
	}
	return m, nil
}

func (m meetingSetupModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if m.step == meetingSetupRoute {
		return m.routeView(w)
	}
	return m.titleView(w)
}

func (m meetingSetupModel) titleView(w int) string {
	var b strings.Builder
	b.WriteString(ansi.Truncate(titleStyle.Render("  Meeting"), w, "…"))
	b.WriteString("\n")
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  STT only — notes · ★ bookmarks · voice EQ"), w, "…"))
	b.WriteString("\n\n")
	b.WriteString(ansi.Truncate(dimStyle.Render("  1/2  Meeting title"), w, "…"))
	b.WriteString("\n  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	if m.err != "" {
		b.WriteString(ansi.Truncate(errorStyle.Render("  "+m.err), w, "…"))
		b.WriteString("\n\n")
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  enter next  •  esc back", w, "…")))
	b.WriteString("\n")
	return b.String()
}

func (m meetingSetupModel) routeView(w int) string {
	var b strings.Builder
	b.WriteString(ansi.Truncate(titleStyle.Render("  Meeting"), w, "…"))
	b.WriteString("\n")
	title := strings.TrimSpace(m.input.Value())
	if title == "" {
		title = "meeting"
	}
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  "+title), w, "…"))
	b.WriteString("\n\n")
	b.WriteString(ansi.Truncate(dimStyle.Render("  2/2  Where should notes go?"), w, "…"))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(dimStyle.Render("  Discovering destinations (camp list, config)…"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render(ansi.Truncate("  esc back", w, "…")))
		b.WriteString("\n")
		return b.String()
	}

	items := []string{
		"keep local only",
		"ask me when the meeting ends",
	}
	for _, d := range m.dests {
		label := meeting.DestinationLabel(d)
		if m.cfg != nil && d.ID == m.cfg.Meeting.Route.Default && d.ID != "" {
			label += " (default)"
		}
		items = append(items, label)
	}
	if len(m.dests) == 0 {
		b.WriteString(dimStyle.Render("  No campaign/file destinations found yet."))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Install camp or add meeting.route.destinations in config."))
		b.WriteString("\n\n")
	}
	for i, label := range items {
		cursor, style := "  ", normalStyle
		if i == m.cursor {
			cursor, style = "▸ ", selectedStyle
		}
		line := "  " + cursor + style.Render(label)
		b.WriteString(ansi.Truncate(line, w, "…"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if m.err != "" {
		b.WriteString(ansi.Truncate(errorStyle.Render("  "+m.err), w, "…"))
		b.WriteString("\n")
	}
	help := fmt.Sprintf("  ↑/↓ choose  •  enter start  •  esc back  ·  %d destination(s)", len(m.dests))
	b.WriteString(dimStyle.Render(ansi.Truncate(help, w, "…")))
	b.WriteString("\n")
	return b.String()
}
