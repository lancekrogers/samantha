package tui

import (
	"context"
	"sync"
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
	player := newFakePreviewPlayer()
	m := newPreviewTestModel(player, &created)

	var cmd tea.Cmd
	m, cmd = updateSettingsWithKey(t, m, "p")
	if cmd == nil {
		t.Fatal("first preview did not return a command")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("first preview command returned nil message")
	}
	m.cursor = 1
	m, cmd = updateSettingsWithKey(t, m, "p")
	if cmd == nil {
		t.Fatal("second preview did not return a command")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("second preview command returned nil message")
	}
	if created != 1 {
		t.Fatalf("preview player created %d times, want 1", created)
	}
	if got := player.playCountValue(); got != 2 {
		t.Fatalf("PlayStream called %d times, want 2", got)
	}
	if player.stopCountValue() == 0 {
		t.Fatal("second preview did not stop the previous playback")
	}
}

func TestCancelledPreviewCommandDoesNotStopSharedPlayerAgain(t *testing.T) {
	var created int
	player := newFakePreviewPlayer()
	done := make(chan audio.PlaybackResult, 1)
	player.queueDone(done)
	m := newPreviewTestModel(player, &created)

	m, cmd := updateSettingsWithKey(t, m, "p")
	if cmd == nil {
		t.Fatal("preview did not return a command")
	}

	msgCh := make(chan tea.Msg, 1)
	go func() {
		msgCh <- cmd()
	}()

	if got := <-player.playStarted; got != 1 {
		t.Fatalf("PlayStream call = %d, want 1", got)
	}

	m.cancelPreview()
	done <- audio.PlaybackResult{Interrupted: true}

	msg, ok := (<-msgCh).(voicePreviewDoneMsg)
	if !ok {
		t.Fatalf("preview command returned %T, want voicePreviewDoneMsg", msg)
	}
	if msg.message != "" {
		t.Fatalf("cancelled preview returned message %q, want quiet completion", msg.message)
	}
	if got := player.stopCountValue(); got != 1 {
		t.Fatalf("Stop called %d times, want only cancelPreview's stop", got)
	}
}

func TestClosePreviewReleasesPlayer(t *testing.T) {
	player := newFakePreviewPlayer()
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

func newPreviewTestModel(player *fakePreviewPlayer, created *int) settingsModel {
	return settingsModel{
		cfg:     &config.Config{},
		section: sectionVoice,
		voiceItems: []tts.Voice{
			{Name: "af_heart"},
			{Name: "af_bella"},
		},
		newPreviewPlayer: func() audio.Engine {
			if created != nil {
				(*created)++
			}
			return player
		},
		ensureTTSAssets: func(context.Context, *config.Config) error {
			return nil
		},
		newTTSProvider: func(*config.Config) (tts.Provider, func(), error) {
			return fakePreviewTTS{}, nil, nil
		},
	}
}

type fakePreviewPlayer struct {
	mu         sync.Mutex
	stopCount  int
	closeCount int
	playCount  int
	doneQueue  []chan audio.PlaybackResult

	playStarted chan int
}

func newFakePreviewPlayer() *fakePreviewPlayer {
	return &fakePreviewPlayer{playStarted: make(chan int, 10)}
}

func (p *fakePreviewPlayer) queueDone(done chan audio.PlaybackResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.doneQueue = append(p.doneQueue, done)
}

func (p *fakePreviewPlayer) PlayStream(context.Context, *audio.PCMStream) (*audio.Playback, error) {
	p.mu.Lock()
	p.playCount++
	call := p.playCount
	var done chan audio.PlaybackResult
	if len(p.doneQueue) > 0 {
		done = p.doneQueue[0]
		p.doneQueue = p.doneQueue[1:]
	}
	p.mu.Unlock()

	if done == nil {
		done = make(chan audio.PlaybackResult, 1)
		done <- audio.PlaybackResult{}
	}
	if p.playStarted != nil {
		p.playStarted <- call
	}

	started := make(chan struct{})
	close(started)
	return audio.NewPlayback(started, done), nil
}

func (p *fakePreviewPlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCount++
}

func (p *fakePreviewPlayer) IsPlaying() bool { return false }

func (p *fakePreviewPlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCount++
	return nil
}

func (p *fakePreviewPlayer) stopCountValue() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCount
}

func (p *fakePreviewPlayer) playCountValue() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playCount
}

type fakePreviewTTS struct{}

func (fakePreviewTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(audio.SampleRate); err != nil {
		return nil, err
	}
	stream.Close()
	return stream, nil
}

func (fakePreviewTTS) Available() bool { return true }

func (fakePreviewTTS) ListVoices(string, string) []tts.Voice { return nil }
