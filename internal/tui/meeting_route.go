package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// meetingRouteModel is the post-meeting destination picker.
type meetingRouteModel struct {
	summary meetinglog.Summary
	cfg     meeting.Config
	dests   []meeting.Destination
	cursor  int
	width   int
	height  int
	// busy is set while a route is in flight.
	busy    bool
	message string
}

// meetingRouteResultMsg closes the picker and returns to the launcher with a banner.
type meetingRouteResultMsg struct {
	Banner string
	IsErr  bool
}

func newMeetingRoute(summary meetinglog.Summary, cfg meeting.Config, dests []meeting.Destination) meetingRouteModel {
	cursor := 0 // 0 = keep local
	// Preselect default destination if present (cursor 1..n maps to dests[0..]).
	if cfg.Default != "" {
		for i, d := range dests {
			if d.ID == cfg.Default {
				cursor = i + 1
				break
			}
		}
	}
	return meetingRouteModel{
		summary: summary,
		cfg:     cfg,
		dests:   dests,
		cursor:  cursor,
	}
}

func (m meetingRouteModel) listLen() int {
	// keep local + each destination
	return 1 + len(m.dests)
}

func (m meetingRouteModel) Update(msg tea.Msg) (meetingRouteModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.busy {
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
		case "esc", "q", "0":
			return m, func() tea.Msg {
				return meetingRouteResultMsg{
					Banner: meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}),
				}
			}
		case "enter":
			if m.cursor == 0 {
				return m, func() tea.Msg {
					return meetingRouteResultMsg{
						Banner: meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}),
					}
				}
			}
			dest := m.dests[m.cursor-1]
			m.busy = true
			m.message = "Routing…"
			summary := m.summary
			body := m.cfg.Body
			cfg := m.cfg
			return m, func() tea.Msg {
				note, err := meeting.Render(summary, body)
				if err != nil {
					return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
				}
				router := meeting.NewDefaultRouter(cfg)
				receipt, err := router.RouteMeeting(context.Background(), note, dest)
				line := meeting.BannerLine(receipt)
				if err != nil {
					return meetingRouteResultMsg{Banner: line, IsErr: true}
				}
				return meetingRouteResultMsg{Banner: line}
			}
		}
	}
	return m, nil
}

func (m meetingRouteModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	b.WriteString(ansi.Truncate(titleStyle.Render("  Route meeting notes"), w, "…"))
	b.WriteString("\n")
	desc := m.summary.Description
	if desc == "" {
		desc = "meeting"
	}
	b.WriteString(ansi.Truncate(subtitleStyle.Render(fmt.Sprintf("  %s · %s", desc, m.summary.Duration().Round(0))), w, "…"))
	b.WriteString("\n\n")

	items := []string{"keep local only"}
	for _, d := range m.dests {
		label := fmt.Sprintf("%s [%s]", d.ID, d.Type)
		if d.ID == m.cfg.Default {
			label += " (default)"
		}
		items = append(items, label)
	}
	for i, label := range items {
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		line := "  " + cursor + style.Render(label)
		b.WriteString(ansi.Truncate(line, w, "…"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if m.message != "" {
		b.WriteString(ansi.Truncate(statusStyle.Render("  "+m.message), w, "…"))
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render(ansi.Truncate("  ↑/↓ navigate  •  enter route  •  esc keep local", w, "…")))
	b.WriteString("\n")
	return b.String()
}
