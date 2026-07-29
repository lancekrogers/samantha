package tui

import (
	"fmt"

	"github.com/lancekrogers/samantha/internal/config"
)

// Speakers settings row indices — keep in lockstep with speakerItems().
const (
	speakerRowLive = iota
	speakerRowThreshold
	speakerRowWindow
	speakerRowMeeting
	speakerRowCount
)

// liveThresholdChoices are the match cutoffs offered for chat labels. Lower
// merges similar voices onto one label; higher splits a voice into a fresh
// speaker-N more readily. Values stay inside the band searchThreshold accepts,
// so every choice reaches the engine unclamped.
var liveThresholdChoices = []float32{0.45, 0.55, 0.6, 0.7}

// liveWindowChoices are analysis window lengths in ms. Shorter labels a turn
// sooner; longer gives the embedder more voice to work with. All are above the
// 0.5s floor StartLiveFeed enforces.
var liveWindowChoices = []int{1000, 1500, 2000, 3000}

// speakerItems lists live conversation speaker analysis — the path that labels
// chat bubbles. Meeting diarization is shown read-only because it is an offline
// per-meeting concern owned by the Meeting tab; surfacing its state here means
// someone looking for "speakers" sees both answers in one place instead of
// concluding the chat toggle is missing.
func (m settingsModel) speakerItems() []string {
	sp := m.cfg.Speaker
	liveOn := sp.Enabled && sp.Live.Enabled
	meetingOn := sp.Enabled && sp.Meeting.Enabled

	threshold := sp.Live.Threshold
	if threshold <= 0 {
		threshold = sp.Threshold
	}
	if threshold <= 0 {
		threshold = 0.6
	}
	window := sp.Live.WindowMS
	if window <= 0 {
		window = 1500
	}

	return []string{
		fmt.Sprintf("Speaker labels in chat — %s", enabledLabel(liveOn)),
		fmt.Sprintf("Match threshold: %.2f", threshold),
		fmt.Sprintf("Analysis window: %d ms", window),
		fmt.Sprintf("Meeting diarization — %s (set in the Meeting tab)", enabledLabel(meetingOn)),
	}
}

func (m *settingsModel) selectSpeakerItem() {
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	switch m.cursor {
	case speakerRowLive:
		m.toggleLiveSpeakers(saveConfig)
	case speakerRowThreshold:
		next := nextLiveThreshold(m.effectiveLiveThreshold())
		if err := saveConfig("speaker.live.threshold", next); err != nil {
			m.message = fmt.Sprintf("Failed to save live threshold: %v", err)
			return
		}
		m.cfg.Speaker.Live.Threshold = next
		m.message = fmt.Sprintf("Match threshold %.2f — lower merges similar voices, higher splits them", next)
	case speakerRowWindow:
		next := nextLiveWindow(m.cfg.Speaker.Live.WindowMS)
		if err := saveConfig("speaker.live.window_ms", next); err != nil {
			m.message = fmt.Sprintf("Failed to save live window: %v", err)
			return
		}
		m.cfg.Speaker.Live.WindowMS = next
		m.message = fmt.Sprintf("Analysis window %d ms", next)
	case speakerRowMeeting:
		m.message = "Meeting diarization is per-meeting — toggle it in Settings → Meeting"
	}
}

// toggleLiveSpeakers flips chat labels. Turning on also turns on the master
// speaker switch, since live analysis is gated behind both; turning off leaves
// the master alone so meeting diarization keeps working.
func (m *settingsModel) toggleLiveSpeakers(saveConfig func(string, any) error) {
	sp := m.cfg.Speaker
	if sp.Enabled && sp.Live.Enabled {
		if err := saveConfig("speaker.live.enabled", false); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.live.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Live.Enabled = false
		m.message = "Speaker labels in chat OFF (meeting diarization unchanged)"
		return
	}
	if !sp.Enabled {
		if err := saveConfig("speaker.enabled", true); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Enabled = true
	}
	if err := saveConfig("speaker.live.enabled", true); err != nil {
		m.message = fmt.Sprintf("Failed to save speaker.live.enabled: %v", err)
		return
	}
	m.cfg.Speaker.Live.Enabled = true
	m.message = "Speaker labels in chat ON — new conversations label turns; in an open chat run /speakers on"
}

// effectiveLiveThreshold resolves the cutoff the engine would actually use,
// mirroring speaker.Config.Normalize's inherit-from-root rule.
func (m settingsModel) effectiveLiveThreshold() float32 {
	if m.cfg.Speaker.Live.Threshold > 0 {
		return m.cfg.Speaker.Live.Threshold
	}
	if m.cfg.Speaker.Threshold > 0 {
		return m.cfg.Speaker.Threshold
	}
	return 0.6
}

// nextLiveThreshold cycles to the next offered cutoff, entering the list at the
// closest value so an out-of-band config lands somewhere sensible.
func nextLiveThreshold(current float32) float32 {
	idx := 0
	best := float32(-1)
	for i, choice := range liveThresholdChoices {
		delta := choice - current
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < best {
			best, idx = delta, i
		}
	}
	return liveThresholdChoices[(idx+1)%len(liveThresholdChoices)]
}

// nextLiveWindow cycles window lengths, treating unset (0) as the default.
func nextLiveWindow(current int) int {
	if current <= 0 {
		current = 1500
	}
	for i, choice := range liveWindowChoices {
		if choice == current {
			return liveWindowChoices[(i+1)%len(liveWindowChoices)]
		}
	}
	return liveWindowChoices[0]
}
