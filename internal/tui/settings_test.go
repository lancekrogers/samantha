package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
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

func TestSettingsLoadsAudioDevices(t *testing.T) {
	m := settingsModel{inputItems: []string{""}, outputItems: []string{""}}
	m, _ = m.Update(deviceListsMsg{
		inputs: []string{"Studio Mic"}, outputs: []string{"Desk Speakers"},
	})
	if m.devicesLoading {
		t.Fatal("device loading flag was not cleared")
	}
	if got := m.inputItems; len(got) != 2 || got[1] != "Studio Mic" {
		t.Fatalf("input items = %v", got)
	}
	if got := m.outputItems; len(got) != 2 || got[1] != "Desk Speakers" {
		t.Fatalf("output items = %v", got)
	}
}

func TestSettingsCompactsForSmallTerminal(t *testing.T) {
	m := settingsModel{
		cfg: &config.Config{}, providerItems: []string{"claude", "ollama", "grok", "other"},
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 36, Height: 8})
	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact settings rendered %d lines in 8-row terminal:\n%s", got, view)
	}
}

func TestSettingsListUsesAllAvailableRows(t *testing.T) {
	items := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	m := settingsModel{cfg: &config.Config{}, providerItems: items}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 13})

	if got := m.visibleRows(); got != 8 {
		t.Fatalf("visible rows = %d, want 8 at a 13-row terminal", got)
	}
	view := stripANSI(m.View())
	for _, item := range items[:8] {
		if !strings.Contains(view, item) {
			t.Errorf("expanded settings view missing %q:\n%s", item, view)
		}
	}
	if strings.Contains(view, items[8]) {
		t.Fatalf("settings rendered more choices than fit:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got != 13 {
		t.Fatalf("settings view has %d rows, want exactly 13:\n%s", got, view)
	}
}

func TestSettingsShowsEntireListWhenItFits(t *testing.T) {
	items := []string{"one", "two", "three", "four", "five", "six"}
	m := settingsModel{cfg: &config.Config{}, providerItems: items}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := stripANSI(m.View())
	for _, item := range items {
		if !strings.Contains(view, item) {
			t.Errorf("settings hid %q even though the list fits:\n%s", item, view)
		}
	}
	if got := len(strings.Split(view, "\n")); got != 24 {
		t.Fatalf("settings view has %d rows, want full 24-row terminal:\n%s", got, view)
	}
}

// Settings chrome and list region must track live terminal resizes (splits,
// full-screen toggles), not only the size captured when the screen opened.
func TestSettingsResizesDynamicallyWithTerminal(t *testing.T) {
	items := make([]string, 40)
	for i := range items {
		items[i] = fmt.Sprintf("provider-%02d", i)
	}
	m := settingsModel{cfg: &config.Config{}, providerItems: items}

	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if got := m.visibleRows(); got != 7 {
		t.Fatalf("visible rows at 12h = %d, want 7", got)
	}
	small := stripANSI(m.View())
	if got := len(strings.Split(small, "\n")); got != 12 {
		t.Fatalf("small terminal view has %d rows, want 12:\n%s", got, small)
	}
	if !strings.Contains(small, strings.Repeat("─", 40)) {
		t.Fatalf("small terminal missing full-width rule:\n%s", small)
	}
	if strings.Contains(small, "provider-07") {
		t.Fatalf("small terminal showed more rows than fit:\n%s", small)
	}

	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if got := m.visibleRows(); got != 35 {
		t.Fatalf("visible rows at 40h = %d, want 35", got)
	}
	large := stripANSI(m.View())
	if got := len(strings.Split(large, "\n")); got != 40 {
		t.Fatalf("large terminal view has %d rows, want 40:\n%s", got, large)
	}
	if !strings.Contains(large, strings.Repeat("─", 120)) {
		t.Fatalf("large terminal missing full-width rule:\n%s", large)
	}
	for _, item := range items[:35] {
		if !strings.Contains(large, item) {
			t.Errorf("expanded settings missing %q after resize", item)
		}
	}
	if strings.Contains(large, "provider-35") {
		t.Fatalf("large terminal showed more rows than fit:\n%s", large)
	}
}

func TestSettingsTTSSectionShowsActiveProviderAndModel(t *testing.T) {
	m := newSettings(&config.Config{
		TTSProvider:   "qwen3-tts",
		QwenTTSModel:  "/opt/qwen/models/1.7b",
		QwenTTSBinary: "/opt/qwen/bin/qwen3-tts-cli",
	}, nil)
	m.section = sectionTTS
	m.width, m.height = 100, 20

	view := stripANSI(m.View())
	for _, want := range []string{"TTS", "kokoro", "managed model", "qwen3-tts", "model 1.7b", "qwen3-tts-cli", "✓"} {
		if !strings.Contains(view, want) {
			t.Errorf("TTS settings missing %q:\n%s", want, view)
		}
	}
}

func TestSettingsTabCycleIncludesTools(t *testing.T) {
	m := settingsModel{cfg: &config.Config{}, section: sectionModel}
	m, _ = updateSettingsWithKey(t, m, "tab")
	if m.section != sectionTools {
		t.Fatalf("section after Model + Tab = %d, want Tools section %d", m.section, sectionTools)
	}
}

func TestSettingsToolsTogglePersistsAndRefreshes(t *testing.T) {
	cfg := &config.Config{
		BrainProvider:     "ollama",
		VoiceToolsEnabled: true,
		SkillsEnabled:     true,
	}
	m := newSettings(cfg, nil)
	m.section = sectionTools
	var savedKey string
	var savedValue any
	m.saveConfig = func(key string, value any) error {
		savedKey, savedValue = key, value
		return nil
	}

	m.selectCurrent()
	if savedKey != "voice_tools_enabled" || savedValue != false {
		t.Fatalf("saved local tools config = %q/%v, want voice_tools_enabled/false", savedKey, savedValue)
	}
	if cfg.VoiceToolsEnabled {
		t.Fatalf("VoiceToolsEnabled = true, want false")
	}
	if !strings.Contains(m.toolItems[0], "OFF") {
		t.Fatalf("local tools row = %q, want OFF", m.toolItems[0])
	}

	m.cursor = 1
	m.selectCurrent()
	if savedKey != "skills_enabled" || savedValue != false {
		t.Fatalf("saved skills config = %q/%v, want skills_enabled/false", savedKey, savedValue)
	}
	if cfg.SkillsEnabled {
		t.Fatalf("SkillsEnabled = true, want false")
	}
	if !strings.Contains(m.toolItems[1], "OFF") {
		t.Fatalf("skills row = %q, want OFF", m.toolItems[1])
	}
}

func TestSettingsToolsSkillsRowOllamaOnly(t *testing.T) {
	cfg := &config.Config{BrainProvider: "claude", SkillsEnabled: false}
	m := newSettings(cfg, nil)
	if !strings.Contains(m.toolItems[1], "n/a") {
		t.Fatalf("non-Ollama skills row = %q, want n/a", m.toolItems[1])
	}
	m.section = sectionTools
	m.cursor = 1
	m.selectCurrent()
	if !strings.Contains(m.message, "Ollama") {
		t.Fatalf("message = %q, want Ollama-only guidance", m.message)
	}
}

func TestSettingsSelectTTSProviderPersistsAndRefreshesVoices(t *testing.T) {
	cfg := &config.Config{TTSProvider: "kokoro"}
	m := newSettings(cfg, nil)
	m.qwenStatus = managedqwen.Status{Installed: true, RuntimeReady: true, ModelReady: true}
	m.buildTTSItems()
	m.section = sectionTTS
	m.cursor = 1 // qwen3-tts
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}

	m.selectCurrent()

	if saved["tts_provider"] != "qwen3-tts" || saved["qwen_tts_mode"] != "customvoice" || saved["qwen_tts_voice"] != "Vivian" {
		t.Fatalf("saved Qwen config = %v", saved)
	}
	if cfg.TTSProvider != "qwen3-tts" {
		t.Fatalf("config TTS provider = %q, want qwen3-tts", cfg.TTSProvider)
	}
	if len(m.voiceItems) != 9 {
		t.Fatalf("Qwen voice items = %d, want 9 model presets", len(m.voiceItems))
	}
	if !strings.Contains(m.message, "immediately") {
		t.Fatalf("selection message = %q, want immediate-activation guidance", m.message)
	}
}

func TestSettingsInstallsQwenBeforeActivatingProvider(t *testing.T) {
	// qwen3-tts-cli with no model was the old persisted default. It now follows
	// the managed setup path so existing users are not stranded after upgrade.
	cfg := &config.Config{TTSProvider: "kokoro", QwenTTSBinary: "qwen3-tts-cli", ModelsDir: t.TempDir()}
	m := newSettings(cfg, nil)
	m.section = sectionTTS
	m.cursor = 1
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.ensureQwen = func(context.Context, string, managedqwen.ProgressFunc) (managedqwen.Status, error) {
		return managedqwen.Status{Installed: true, RuntimeReady: true, ModelReady: true}, nil
	}

	cmd := m.selectCurrent()
	if cmd == nil || !m.qwenInstalling {
		t.Fatal("selecting an uninstalled Qwen provider did not start managed setup")
	}
	if cfg.TTSProvider != "kokoro" || len(saved) != 0 {
		t.Fatalf("provider changed before setup completed: cfg=%q saved=%v", cfg.TTSProvider, saved)
	}
	// Execute the install command directly; selectCurrent returns it batched
	// with the progress-feed listener used by the live Bubble Tea program.
	m, _ = m.Update(m.installManagedQwen(context.Background())())
	if cfg.TTSProvider != "qwen3-tts" || saved["tts_provider"] != "qwen3-tts" {
		t.Fatalf("successful setup did not activate Qwen: cfg=%q saved=%v", cfg.TTSProvider, saved)
	}
	if len(m.voiceItems) != 9 || m.voiceItems[0].Name != "Vivian" {
		t.Fatalf("installed voices = %+v, want Qwen presets", m.voiceItems)
	}
}

func TestSettingsReportsQwenInstallProgress(t *testing.T) {
	m := settingsModel{qwenInstalling: true, qwenInstallEvents: newEventBridge(2)}
	m, cmd := m.Update(qwenInstallProgressMsg{stage: "Qwen CustomVoice model", pct: 55})
	if !strings.Contains(m.message, "Qwen CustomVoice model") || !strings.Contains(m.message, "55%") {
		t.Fatalf("progress message = %q", m.message)
	}
	if cmd == nil {
		t.Fatal("progress listener was not re-armed")
	}
}

func TestSettingsQwenVoiceSectionExplainsUnavailableModes(t *testing.T) {
	m := newSettings(&config.Config{TTSProvider: "qwen3-tts", ModelsDir: t.TempDir()}, nil)
	m.section = sectionVoice
	m.width, m.height = 100, 20

	view := stripANSI(m.View())
	if !strings.Contains(view, "not installed") || !strings.Contains(view, "press enter") {
		t.Fatalf("Qwen voice section = %q, want managed-install guidance", view)
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
