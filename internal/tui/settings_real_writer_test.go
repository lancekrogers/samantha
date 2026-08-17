package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// The Settings tests elsewhere stub m.saveConfig, which proves the model calls
// a writer but never that the real one accepts what it is handed. These tests
// deliberately leave the stub off: they run config.SetAndSave against a
// throwaway install root and read the file back. Both cover keys the settings
// schema marks non-editable — the TUI is their manager, and a generic-setter
// guard must not reach it.

// realWriterInstall points config at a throwaway root seeded with body and
// returns the config file path.
func realWriterInstall(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	config.ResetForTest(t)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	if _, err := config.LoadRaw(); err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	return path
}

func readWritten(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	return string(data)
}

func TestSettingsMouseToggleWritesThroughTheRealWriter(t *testing.T) {
	path := realWriterInstall(t, "# keep me\ntui_mouse_enabled: false\n")

	cfg := &config.Config{}
	m := newSettings(cfg, nil)
	m.section = sectionTools
	m.cursor = toolRowMouse
	m.selectCurrent()

	if m.message != "" && strings.Contains(m.message, "Failed to save") {
		t.Fatalf("real writer refused the TUI's own toggle: %s", m.message)
	}
	if !cfg.TUIMouseEnabled {
		t.Error("live config not updated")
	}
	written := readWritten(t, path)
	if !strings.Contains(written, "tui_mouse_enabled: true") {
		t.Errorf("value not persisted:\n%s", written)
	}
	if !strings.Contains(written, "# keep me") {
		t.Errorf("comment lost:\n%s", written)
	}
	if got := config.Get("tui_mouse_enabled"); got != true {
		t.Errorf("in-process value = %v, want true", got)
	}
}

func TestSettingsDestinationPersistsThroughTheRealWriter(t *testing.T) {
	path := realWriterInstall(t, "# keep me\nmeeting:\n  route:\n    mode: ask\n")

	cfg := &config.Config{}
	m := newSettings(cfg, nil)
	dest := meeting.Destination{ID: "campaign:My_Tools", Type: "campaign", Campaign: "My_Tools"}

	if err := m.ensureDestinationConfigured(dest); err != nil {
		t.Fatalf("real writer refused a destination the meeting editor owns: %v", err)
	}

	written := readWritten(t, path)
	if !strings.Contains(written, "destinations:") || !strings.Contains(written, "My_Tools") {
		t.Errorf("destination not persisted:\n%s", written)
	}
	if !strings.Contains(written, "# keep me") {
		t.Errorf("comment lost:\n%s", written)
	}
	if !strings.Contains(written, "mode: ask") {
		t.Errorf("sibling meeting.route key clobbered:\n%s", written)
	}

	// It must survive a reload as a real destination, not as text.
	reloaded, err := config.LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if len(reloaded.Meeting.Route.Destinations) != 1 {
		t.Fatalf("destinations = %+v, want one", reloaded.Meeting.Route.Destinations)
	}
	got := reloaded.Meeting.Route.Destinations[0]
	if got.ID != dest.ID || got.Type != "campaign" || got.Campaign != "My_Tools" {
		t.Errorf("destination round-tripped as %+v", got)
	}
	if got.Capture == "" {
		t.Error("normalized capture was not persisted")
	}

	// A second call for the same destination is a no-op, not a duplicate.
	if err := m.ensureDestinationConfigured(dest); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if strings.Count(readWritten(t, path), "- id: "+dest.ID) != 1 {
		t.Errorf("destination duplicated:\n%s", readWritten(t, path))
	}
}
