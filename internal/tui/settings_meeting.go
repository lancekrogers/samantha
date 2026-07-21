package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
)

// meetingItems lists Meeting Notes settings rows (mode, default, body, discovery).
func (m settingsModel) meetingItems() []string {
	mode := m.cfg.Meeting.Route.Mode
	if mode == "" {
		mode = "ask"
	}
	def := m.cfg.Meeting.Route.Default
	if def == "" {
		def = "(none)"
	}
	body := m.cfg.Meeting.Route.Body
	if body == "" {
		body = "notes"
	}
	nCfg := len(m.cfg.Meeting.Route.Destinations)
	nAvail := len(m.routeDests)
	availLabel := "…"
	if m.routeDestsLoading {
		availLabel = "discovering…"
	} else if m.routeDestsErr != "" {
		availLabel = "discovery error"
	} else {
		availLabel = fmt.Sprintf("%d available", nAvail)
	}
	return []string{
		fmt.Sprintf("Route mode: %s", mode),
		fmt.Sprintf("Default destination: %s", def),
		fmt.Sprintf("Body scope: %s", body),
		fmt.Sprintf("Configured destinations: %d", nCfg),
		fmt.Sprintf("Picker destinations: %s", availLabel),
		"Refresh destinations (camp list + config)",
	}
}

func (m *settingsModel) selectMeetingItem() {
	switch m.cursor {
	case 0: // cycle route mode: ask → auto → off → ask
		cur := m.cfg.Meeting.Route.Mode
		if cur == "" {
			cur = "ask"
		}
		next := map[string]string{"ask": "auto", "auto": "off", "off": "ask"}[cur]
		if next == "" {
			next = "ask"
		}
		if err := config.SetAndSave("meeting.route.mode", next); err != nil {
			m.message = fmt.Sprintf("Failed to save route mode: %v", err)
			return
		}
		m.cfg.Meeting.Route.Mode = next
		m.message = fmt.Sprintf("Meeting route mode: %s", next)
	case 1: // cycle default among configured + discovered ids + empty
		ids := []string{""}
		seen := map[string]struct{}{"": {}}
		for _, d := range m.cfg.Meeting.Route.Destinations {
			if d.ID == "" {
				continue
			}
			if _, ok := seen[d.ID]; ok {
				continue
			}
			seen[d.ID] = struct{}{}
			ids = append(ids, d.ID)
		}
		for _, d := range m.routeDests {
			if d.ID == "" {
				continue
			}
			if _, ok := seen[d.ID]; ok {
				continue
			}
			seen[d.ID] = struct{}{}
			ids = append(ids, d.ID)
		}
		cur := m.cfg.Meeting.Route.Default
		idx := 0
		for i, id := range ids {
			if id == cur {
				idx = i
				break
			}
		}
		next := ids[(idx+1)%len(ids)]
		if err := config.SetAndSave("meeting.route.default", next); err != nil {
			m.message = fmt.Sprintf("Failed to save default destination: %v", err)
			return
		}
		m.cfg.Meeting.Route.Default = next
		label := next
		if label == "" {
			label = "(none)"
		}
		m.message = fmt.Sprintf("Default destination: %s", label)
	case 2: // cycle body: notes ↔ full
		cur := m.cfg.Meeting.Route.Body
		if cur == "" {
			cur = "notes"
		}
		next := "full"
		if cur == "full" {
			next = "notes"
		}
		if err := config.SetAndSave("meeting.route.body", next); err != nil {
			m.message = fmt.Sprintf("Failed to save body scope: %v", err)
			return
		}
		m.cfg.Meeting.Route.Body = next
		m.message = fmt.Sprintf("Meeting body scope: %s", next)
	case 3:
		m.message = "Configured destinations live in " + config.ConfigFile() + " (meeting.route.destinations)"
	case 4:
		if m.routeDestsLoading {
			m.message = "Still discovering destinations…"
			return
		}
		if m.routeDestsErr != "" {
			m.message = "Discovery error: " + m.routeDestsErr
			return
		}
		if len(m.routeDests) == 0 {
			m.message = "No destinations yet — install camp or add YAML destinations; start a meeting to pick"
			return
		}
		// Show a short preview of the first few labels.
		n := min(len(m.routeDests), 3)
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			parts = append(parts, meeting.DestinationLabel(m.routeDests[i]))
		}
		more := ""
		if len(m.routeDests) > n {
			more = fmt.Sprintf(" (+%d more)", len(m.routeDests)-n)
		}
		m.message = fmt.Sprintf("%d destination(s): %v%s — choose when starting a meeting", len(m.routeDests), parts, more)
	case 5:
		m.message = "Refreshing destinations…"
	}
}

// loadRouteDestinations probes camp list + config for the Meeting settings tab.
func (m *settingsModel) loadRouteDestinations() tea.Cmd {
	m.routeDestsLoading = true
	m.routeDestsErr = ""
	m.routeDestsSeq++
	seq := m.routeDestsSeq
	cfg := m.cfg
	return func() tea.Msg {
		routeCfg := meeting.FromConfig(cfg)
		router := meeting.NewDefaultRouter(routeCfg)
		ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
		defer cancel()
		dests := router.DiscoverDestinations(ctx)
		return meetingRouteDestsMsg{seq: seq, dests: dests}
	}
}

// meetingRouteDestsMsg is async discovery for Settings → Meeting.
type meetingRouteDestsMsg struct {
	seq   int
	dests []meeting.Destination
	err   error
}
