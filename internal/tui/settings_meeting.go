package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
)

// Meeting settings row indices — keep in lockstep with meetingItems().
const (
	meetingRowDiarization = iota
	meetingRowRecordAudio
	meetingRowMode
	meetingRowDefault
	meetingRowBody
	meetingRowConfigured
	meetingRowPicker
	meetingRowRefresh
	meetingRowCount
)

// meetingItems lists Meeting settings: speaker diarization + notes routing.
// Live conversation speaker analysis is controlled in chat via /speakers, not here.
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
	// Effective meeting diarization needs master speaker.enabled + meeting flag.
	diarizationOn := m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Enabled
	recordAudioOn := m.cfg.Speaker.Meeting.RecordAudio
	return []string{
		fmt.Sprintf("Speaker diarization — %s", enabledLabel(diarizationOn)),
		fmt.Sprintf("Record audio for analysis — %s", enabledLabel(recordAudioOn)),
		fmt.Sprintf("Route mode: %s", mode),
		fmt.Sprintf("Default destination: %s", def),
		fmt.Sprintf("Body scope: %s", body),
		fmt.Sprintf("Configured destinations: %d", nCfg),
		fmt.Sprintf("Picker destinations: %s", availLabel),
		"Refresh destinations (camp list + config)",
	}
}

func (m *settingsModel) selectMeetingItem() {
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	switch m.cursor {
	case meetingRowDiarization:
		// Toggle effective meeting diarization: turns master + meeting on together,
		// or meeting off (master left as-is for live /speakers path).
		next := !(m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Enabled)
		if next {
			if err := saveConfig("speaker.enabled", true); err != nil {
				m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
				return
			}
			m.cfg.Speaker.Enabled = true
			if err := saveConfig("speaker.meeting.enabled", true); err != nil {
				m.message = fmt.Sprintf("Failed to save speaker.meeting.enabled: %v", err)
				return
			}
			m.cfg.Speaker.Meeting.Enabled = true
			m.message = "Speaker diarization ON — models install when the next meeting starts"
			return
		}
		if err := saveConfig("speaker.meeting.enabled", false); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.Enabled = false
		m.message = "Speaker diarization OFF (live /speakers unchanged)"
	case meetingRowRecordAudio:
		next := !m.cfg.Speaker.Meeting.RecordAudio
		if err := saveConfig("speaker.meeting.record_audio", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.record_audio: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.RecordAudio = next
		m.message = fmt.Sprintf("Record audio for analysis %s", enabledLabel(next))
	case meetingRowMode: // cycle route mode: ask → auto → off → ask
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
	case meetingRowDefault:
		m.cycleDefaultDestination()
	case meetingRowBody: // cycle body: notes ↔ full
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
	case meetingRowConfigured:
		m.message = "Configured destinations live in " + config.ConfigFile() + " (meeting.route.destinations)"
	case meetingRowPicker:
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
	case meetingRowRefresh:
		m.message = "Refreshing destinations…"
	}
}

// cycleDefaultDestination walks empty + configured + discovered IDs. When the
// chosen ID is discovered-only, it is also persisted into
// meeting.route.destinations so mode=auto / RouteByID can resolve it.
func (m *settingsModel) cycleDefaultDestination() {
	type choice struct {
		id   string
		dest *meeting.Destination // non-nil when we may need to persist
	}
	choices := []choice{{id: ""}}
	seen := map[string]struct{}{"": {}}
	for _, d := range m.cfg.Meeting.Route.Destinations {
		if d.ID == "" {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		choices = append(choices, choice{id: d.ID})
	}
	for i := range m.routeDests {
		d := m.routeDests[i]
		if d.ID == "" {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		dd := d
		choices = append(choices, choice{id: d.ID, dest: &dd})
	}
	cur := m.cfg.Meeting.Route.Default
	idx := 0
	for i, c := range choices {
		if c.id == cur {
			idx = i
			break
		}
	}
	next := choices[(idx+1)%len(choices)]

	// Persist discovered destinations before setting them as default.
	if next.dest != nil {
		if err := m.ensureDestinationConfigured(*next.dest); err != nil {
			m.message = fmt.Sprintf("Failed to save destination: %v", err)
			return
		}
	}
	if err := config.SetAndSave("meeting.route.default", next.id); err != nil {
		m.message = fmt.Sprintf("Failed to save default destination: %v", err)
		return
	}
	m.cfg.Meeting.Route.Default = next.id
	label := next.id
	if label == "" {
		label = "(none)"
	}
	m.message = fmt.Sprintf("Default destination: %s", label)
}

// ensureDestinationConfigured appends dest to meeting.route.destinations when
// missing so auto/RouteByID can resolve camp-discovered IDs after restart.
func (m *settingsModel) ensureDestinationConfigured(dest meeting.Destination) error {
	for _, d := range m.cfg.Meeting.Route.Destinations {
		if d.ID == dest.ID {
			return nil
		}
	}
	entry := config.MeetingDestinationConfig{
		ID:       dest.ID,
		Type:     dest.Type,
		Campaign: dest.Campaign,
		Capture:  dest.Capture,
		Tags:     dest.Tags,
		Path:     dest.Path,
		Folder:   dest.Folder,
	}
	next := append(append([]config.MeetingDestinationConfig{}, m.cfg.Meeting.Route.Destinations...), entry)
	if err := config.SetAndSave("meeting.route.destinations", next); err != nil {
		return err
	}
	m.cfg.Meeting.Route.Destinations = next
	return nil
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
		dests, err := router.DiscoverDestinations(ctx)
		return meetingRouteDestsMsg{seq: seq, dests: dests, err: err}
	}
}

// meetingRouteDestsMsg is async discovery for Settings → Meeting.
type meetingRouteDestsMsg struct {
	seq   int
	dests []meeting.Destination
	err   error
}
