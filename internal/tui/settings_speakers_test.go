package tui

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

// newSpeakerSettings builds a Speakers-section model with a recording saveConfig.
func newSpeakerSettings(t *testing.T, cfg *config.Config, row int) (*settingsModel, map[string]any) {
	t.Helper()
	m := newSettings(cfg, nil)
	m.section = sectionSpeakers
	m.cursor = row
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	return &m, saved
}

// TestSettingsSpeakersToggleEnablesLive is the gap this section closes: chat
// speaker labels were reachable only by hand-editing config.yaml.
func TestSettingsSpeakersToggleEnablesLive(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if saved["speaker.enabled"] != true {
		t.Errorf("master speaker.enabled saved = %v, want true", saved["speaker.enabled"])
	}
	if saved["speaker.live.enabled"] != true {
		t.Errorf("speaker.live.enabled saved = %v, want true", saved["speaker.live.enabled"])
	}
	if !cfg.Speaker.Enabled || !cfg.Speaker.Live.Enabled {
		t.Errorf("cfg speaker = %+v, want master and live on", cfg.Speaker)
	}
	// The running conversation needs /speakers on; only new chats pick it up
	// from config, and the message must not imply otherwise.
	if !strings.Contains(m.message, "/speakers on") {
		t.Errorf("message = %q, want the /speakers on hint", m.message)
	}
}

// TestSettingsSpeakersDisableLiveKeepsMeeting guards the asymmetry: live off
// must not switch off the master flag that meeting diarization also needs.
func TestSettingsSpeakersDisableLiveKeepsMeeting(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if saved["speaker.live.enabled"] != false {
		t.Errorf("speaker.live.enabled saved = %v, want false", saved["speaker.live.enabled"])
	}
	if _, ok := saved["speaker.enabled"]; ok {
		t.Errorf("disabling live must not write speaker.enabled, saved=%v", saved)
	}
	if !cfg.Speaker.Enabled || !cfg.Speaker.Meeting.Enabled {
		t.Errorf("cfg speaker = %+v, want meeting diarization intact", cfg.Speaker)
	}
}

// TestSettingsSpeakersEnableLiveKeepsExistingMaster covers the common path from
// the Meeting tab: master already on, so only the live key should be written.
func TestSettingsSpeakersEnableLiveKeepsExistingMaster(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if _, ok := saved["speaker.enabled"]; ok {
		t.Errorf("master was already on; should not be rewritten: %v", saved)
	}
	if saved["speaker.live.enabled"] != true {
		t.Errorf("speaker.live.enabled saved = %v, want true", saved["speaker.live.enabled"])
	}
}

func TestSettingsSpeakersThresholdCycles(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Live.Threshold = 0.55
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	got, ok := saved["speaker.live.threshold"].(float32)
	if !ok {
		t.Fatalf("speaker.live.threshold saved = %#v, want float32", saved["speaker.live.threshold"])
	}
	if got != 0.6 {
		t.Errorf("threshold cycled to %v, want 0.60 (next after 0.55)", got)
	}
	if cfg.Speaker.Live.Threshold != got {
		t.Errorf("cfg threshold = %v, want %v", cfg.Speaker.Live.Threshold, got)
	}
}

// TestSettingsSpeakersThresholdCycleWraps proves the last choice returns to the
// first rather than sticking.
func TestSettingsSpeakersThresholdCycleWraps(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Live.Threshold = 0.7
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	if got := saved["speaker.live.threshold"]; got != float32(0.45) {
		t.Errorf("threshold after 0.70 = %v, want 0.45", got)
	}
}

// TestSettingsSpeakersThresholdInheritsRoot mirrors Normalize's inherit rule:
// an unset live threshold cycles from the root value, not from zero.
func TestSettingsSpeakersThresholdInheritsRoot(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Threshold = 0.7
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	if got := saved["speaker.live.threshold"]; got != float32(0.45) {
		t.Errorf("threshold = %v, want 0.45 (cycled from inherited 0.70)", got)
	}
}

func TestSettingsSpeakersWindowCycles(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowWindow)

	// Unset window is the 1500ms default, so one press moves to 2000.
	m.selectCurrent()

	if got := saved["speaker.live.window_ms"]; got != 2000 {
		t.Errorf("window_ms saved = %v, want 2000", got)
	}
	if cfg.Speaker.Live.WindowMS != 2000 {
		t.Errorf("cfg window = %d, want 2000", cfg.Speaker.Live.WindowMS)
	}
}

// TestSettingsSpeakersMeetingRowIsReadOnly keeps ownership clear: meeting
// diarization is shown for orientation but changed in the Meeting tab.
func TestSettingsSpeakersMeetingRowIsReadOnly(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowMeeting)

	m.selectCurrent()

	if len(saved) != 0 {
		t.Errorf("meeting row wrote config: %v", saved)
	}
	if !strings.Contains(m.message, "Meeting") {
		t.Errorf("message = %q, want a pointer to the Meeting tab", m.message)
	}
}

// TestSettingsSpeakersViewShowsState is the discoverability check — the user
// reported not finding any speakers setting in the TUI.
func TestSettingsSpeakersViewShowsState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m := newSettings(cfg, nil)
	m.section = sectionSpeakers

	view := stripANSI(m.View())

	for _, want := range []string{"Speakers", "Speaker labels in chat", "Match threshold", "Analysis window", "Meeting diarization"} {
		if !strings.Contains(view, want) {
			t.Errorf("speakers view missing %q:\n%s", want, view)
		}
	}
}

// TestSpeakerItemsRowCountMatchesConstants keeps the row indices honest; a row
// added without updating the constants would silently misroute Enter.
func TestSpeakerItemsRowCountMatchesConstants(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	if got := len(m.speakerItems()); got != speakerRowCount {
		t.Errorf("speakerItems() has %d rows, speakerRowCount = %d", got, speakerRowCount)
	}
}
