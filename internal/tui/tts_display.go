package tui

import (
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/tts"
)

// activeTTSProvider returns the normalized provider name shown by the TUI.
// An empty provider is the config default: Kokoro.
func activeTTSProvider(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.TTSProvider) == "" {
		return "kokoro"
	}
	return strings.ToLower(strings.TrimSpace(cfg.TTSProvider))
}

// ttsModelLabel returns a compact model description suitable for a status chip.
// Qwen is configured with a directory, so the final path component is the most
// useful display value while keeping the badge readable.
func ttsModelLabel(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return ttsModelLabelForProvider(activeTTSProvider(cfg), cfg)
}

func ttsModelLabelForProvider(provider string, cfg *config.Config) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kokoro":
		return "managed"
	case "qwen3-tts":
		if cfg == nil {
			return "unset"
		}
		model := strings.TrimSpace(cfg.QwenTTSModel)
		if model == "" {
			return "unset"
		}
		return filepath.Base(filepath.Clean(model))
	default:
		return "unconfigured"
	}
}

func ttsBinaryLabel(cfg *config.Config) string {
	if cfg == nil {
		return "qwen3-tts-cli"
	}
	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	if binary == "" {
		binary = "qwen3-tts-cli"
	}
	return filepath.Base(filepath.Clean(binary))
}

// ttsBadgeLabel is used on the launcher and conversation header so the active
// TTS engine remains visible outside Settings.
func ttsBadgeLabel(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return "tts " + activeTTSProvider(cfg) + " · " + ttsModelLabel(cfg)
}

func ttsProviderDetail(spec tts.ProviderSpec, cfg *config.Config) string {
	switch strings.ToLower(strings.TrimSpace(spec.Name)) {
	case "kokoro":
		return "managed model · static voices"
	case "qwen3-tts":
		return "model " + ttsModelLabelForProvider("qwen3-tts", cfg) + " · " + ttsBinaryLabel(cfg)
	default:
		return spec.Description
	}
}
