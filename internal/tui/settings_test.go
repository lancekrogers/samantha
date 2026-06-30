package tui

import "testing"

// A preview completion that belongs to a superseded voice must not clear the
// current "playing" indicator or overwrite its message.
func TestVoicePreviewDoneGating(t *testing.T) {
	m := settingsModel{previewing: "af_bella", message: "playing"}

	m, _ = m.Update(voicePreviewDoneMsg{voice: "af_heart", message: "Previewed af_heart"})
	if m.previewing != "af_bella" {
		t.Fatalf("stale completion cleared previewing: got %q", m.previewing)
	}
	if m.message != "playing" {
		t.Fatalf("stale completion overwrote message: got %q", m.message)
	}

	m, _ = m.Update(voicePreviewDoneMsg{voice: "af_bella", message: "Previewed af_bella"})
	if m.previewing != "" {
		t.Fatalf("matching completion did not clear previewing: got %q", m.previewing)
	}
	if m.message != "Previewed af_bella" {
		t.Fatalf("matching completion did not set message: got %q", m.message)
	}
}
