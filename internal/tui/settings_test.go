package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/tts"
)

// A preview completion that belongs to a superseded voice must not clear the
// current "playing" indicator or overwrite its message.
func TestVoicePreviewDoneGating(t *testing.T) {
	m := settingsModel{previewing: "af_bella", previewID: 2, message: "playing"}

	m, _ = m.Update(voicePreviewDoneMsg{id: 1, voice: "af_heart", message: "Previewed af_heart"})
	if m.previewing != "af_bella" {
		t.Fatalf("stale completion cleared previewing: got %q", m.previewing)
	}
	if m.message != "playing" {
		t.Fatalf("stale completion overwrote message: got %q", m.message)
	}

	m, _ = m.Update(voicePreviewDoneMsg{id: 2, voice: "af_bella", message: "Previewed af_bella"})
	if m.previewing != "" {
		t.Fatalf("matching completion did not clear previewing: got %q", m.previewing)
	}
	if m.message != "Previewed af_bella" {
		t.Fatalf("matching completion did not set message: got %q", m.message)
	}
}

func TestVoicePreviewDoneGatingSameVoice(t *testing.T) {
	m := settingsModel{previewing: "af_heart", previewID: 2, message: "playing"}

	m, _ = m.Update(voicePreviewDoneMsg{id: 1, voice: "af_heart", message: "Previewed af_heart"})
	if m.previewing != "af_heart" {
		t.Fatalf("stale same-voice completion cleared previewing: got %q", m.previewing)
	}
	if m.message != "playing" {
		t.Fatalf("stale same-voice completion overwrote message: got %q", m.message)
	}
}

func TestCancelPreviewNilSafe(t *testing.T) {
	m := settingsModel{}
	m.cancelPreview()

	called := false
	m.previewCancel = func() { called = true }
	m.cancelPreview()
	if !called {
		t.Fatal("cancelPreview did not invoke previewCancel")
	}
}

func TestVoicePreviewReusesPlayerAcrossPreviews(t *testing.T) {
	var created int
	player := &fakePreviewPlayer{}
	m := settingsModel{
		cfg:     &config.Config{},
		section: sectionVoice,
		voiceItems: []tts.Voice{
			{Name: "af_heart"},
			{Name: "af_bella"},
		},
		newPreviewPlayer: func() audio.Engine {
			created++
			return player
		},
	}

	var cmd tea.Cmd
	m, cmd = updateSettingsWithKey(t, m, "p")
	if cmd == nil {
		t.Fatal("first preview did not return a command")
	}
	m.cursor = 1
	m, cmd = updateSettingsWithKey(t, m, "p")
	if cmd == nil {
		t.Fatal("second preview did not return a command")
	}
	if created != 1 {
		t.Fatalf("preview player created %d times, want 1", created)
	}
	if player.stopCount == 0 {
		t.Fatal("second preview did not stop the previous playback")
	}
}

func TestClosePreviewReleasesPlayer(t *testing.T) {
	player := &fakePreviewPlayer{}
	cancelled := false
	m := settingsModel{
		previewCancel: func() { cancelled = true },
		previewPlayer: player,
	}

	m.closePreview()
	if !cancelled {
		t.Fatal("closePreview did not cancel the active preview")
	}
	if player.closeCount != 1 {
		t.Fatalf("player close count = %d, want 1", player.closeCount)
	}
	if m.previewPlayer != nil {
		t.Fatal("closePreview did not clear previewPlayer")
	}
}

func updateSettingsWithKey(t *testing.T, m settingsModel, key string) (settingsModel, tea.Cmd) {
	t.Helper()
	return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

type fakePreviewPlayer struct {
	stopCount  int
	closeCount int
}

func (p *fakePreviewPlayer) PlayStream(context.Context, *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	close(started)
	done := make(chan audio.PlaybackResult, 1)
	done <- audio.PlaybackResult{}
	return audio.NewPlayback(started, done), nil
}

func (p *fakePreviewPlayer) Stop()           { p.stopCount++ }
func (p *fakePreviewPlayer) IsPlaying() bool { return false }
func (p *fakePreviewPlayer) Close() error {
	p.closeCount++
	return nil
}
