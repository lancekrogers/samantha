package tui

import (
	"fmt"

	"github.com/lancekrogers/samantha/internal/config"
)

// speakerItems lists Speaker analysis settings rows.
func (m settingsModel) speakerItems() []string {
	master := enabledLabel(m.cfg.Speaker.Enabled)
	meetingOn := enabledLabel(m.cfg.Speaker.Meeting.Enabled)
	liveOn := enabledLabel(m.cfg.Speaker.Live.Enabled)
	mode := m.cfg.Speaker.Live.Mode
	if mode == "" {
		mode = "indicator"
	}
	return []string{
		fmt.Sprintf("Speaker analysis — %s", master),
		fmt.Sprintf("Meeting diarization — %s", meetingOn),
		fmt.Sprintf("Live conversation — %s", liveOn),
		fmt.Sprintf("Live mode: %s", mode),
	}
}

func (m *settingsModel) selectSpeakerItem() {
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	switch m.cursor {
	case 0:
		next := !m.cfg.Speaker.Enabled
		if err := saveConfig("speaker.enabled", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Enabled = next
		m.message = fmt.Sprintf("Speaker analysis %s", enabledLabel(next))
	case 1:
		next := !m.cfg.Speaker.Meeting.Enabled
		if err := saveConfig("speaker.meeting.enabled", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.Enabled = next
		// Master off but meeting on is confusing — turn master on when enabling meeting.
		if next && !m.cfg.Speaker.Enabled {
			if err := saveConfig("speaker.enabled", true); err == nil {
				m.cfg.Speaker.Enabled = true
			}
		}
		m.message = fmt.Sprintf("Meeting diarization %s", enabledLabel(next))
	case 2:
		next := !m.cfg.Speaker.Live.Enabled
		if err := saveConfig("speaker.live.enabled", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.live.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Live.Enabled = next
		if next && !m.cfg.Speaker.Enabled {
			if err := saveConfig("speaker.enabled", true); err == nil {
				m.cfg.Speaker.Enabled = true
			}
		}
		m.message = fmt.Sprintf("Live speaker analysis %s", enabledLabel(next))
	case 3:
		cur := m.cfg.Speaker.Live.Mode
		if cur == "" {
			cur = "indicator"
		}
		next := "owner_verify"
		if cur == "owner_verify" {
			next = "indicator"
		}
		if err := saveConfig("speaker.live.mode", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.live.mode: %v", err)
			return
		}
		m.cfg.Speaker.Live.Mode = next
		m.message = fmt.Sprintf("Live mode: %s", next)
	}
}
