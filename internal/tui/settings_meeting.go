package tui

import (
	"fmt"

	"github.com/lancekrogers/samantha/internal/config"
)

// meetingItems lists Meeting Notes settings rows (mode, default, body).
// Destination CRUD stays in YAML for v1.
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
	nDest := len(m.cfg.Meeting.Route.Destinations)
	return []string{
		fmt.Sprintf("Route mode: %s", mode),
		fmt.Sprintf("Default destination: %s", def),
		fmt.Sprintf("Body scope: %s", body),
		fmt.Sprintf("Destinations: %d (edit config.yaml)", nDest),
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
	case 1: // cycle default destination among configured ids + empty
		dests := m.cfg.Meeting.Route.Destinations
		ids := make([]string, 0, len(dests)+1)
		ids = append(ids, "")
		for _, d := range dests {
			if d.ID != "" {
				ids = append(ids, d.ID)
			}
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
		m.message = "Edit meeting.route.destinations in " + config.ConfigFile()
	}
}
