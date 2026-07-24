package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/tts"
)

// cancelPreview stops any in-flight voice preview. Safe to call when idle.
func (m *settingsModel) cancelPreview() {
	if m.previewCancel != nil {
		m.previewCancel()
		m.previewCancel = nil
	}
	if m.previewPlayer != nil {
		m.previewPlayer.Stop()
	}
}

func (m *settingsModel) closePreview() {
	m.cancelPreview()
	if m.qwenInstallCancel != nil {
		m.qwenInstallCancel()
		m.qwenInstallCancel = nil
		m.qwenInstalling = false
		m.qwenInstallEvents = nil
	}
	if m.previewPlayer != nil {
		_ = m.previewPlayer.Close()
		m.previewPlayer = nil
	}
}

func (m *settingsModel) playerForPreview() audio.Engine {
	if m.previewPlayer != nil {
		return m.previewPlayer
	}
	m.previewPlayer = m.newPreviewPlayer()
	return m.previewPlayer
}

type voicePreviewDoneMsg struct {
	id      int64
	voice   string
	message string
}

func (m settingsModel) previewVoice(ctx context.Context, id int64, voice tts.Voice, player audio.Engine) tea.Cmd {
	// Snapshot the config before the closure runs: the returned Cmd executes on
	// its own goroutine while selectCurrent keeps mutating m.cfg on Update's.
	cfg := *m.cfg
	if activeTTSProvider(&cfg) == "qwen3-tts" {
		cfg.QwenTTSMode = string(tts.VoiceModeCustomVoice)
		cfg.QwenTTSVoice = voice.Name
		if cfg.QwenTTSLanguage == "" {
			cfg.QwenTTSLanguage = "Auto"
		}
	} else {
		cfg.TTSVoice = voice.Name
	}
	return func() tea.Msg {
		// A superseded preview (ctx cancelled) reports quietly so it doesn't
		// clobber the newer preview's message or "playing" indicator.
		quiet := voicePreviewDoneMsg{id: id, voice: voice.Name}

		if err := m.ensureTTSAssets(ctx, &cfg); err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Asset error: %v", err)}
		}

		ttsProvider, cleanup, err := m.newTTSProvider(&cfg)
		if err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("TTS error: %v", err)}
		}
		if cleanup != nil {
			defer cleanup()
		}

		// Name the voice being auditioned — not a fixed persona. Providers,
		// voices, and agents are all multi-select now.
		stream, err := ttsProvider.Synthesize(ctx, tts.SpokenPreviewLine(voice.Name))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Synthesize error: %v", err)}
		}

		playback, err := player.PlayStream(ctx, stream)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", err)}
		}

		var result audio.PlaybackResult
		select {
		case <-ctx.Done():
			// cancelPreview already stopped the shared player. Do not stop it
			// again here, because a newer preview may now be queued on it.
			<-playback.Done()
			return quiet
		case result = <-playback.Done():
		}
		if result.Interrupted || errors.Is(result.Err, context.Canceled) {
			return quiet
		}
		if result.Err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", result.Err)}
		}

		return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Previewed %s", voice.Name)}
	}
}
