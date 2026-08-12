package tui

import (
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
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
		// Managed selection (empty binary/model) prefers native package when present.
		if qwenManagedNativeReady(cfg) {
			tier := strings.TrimSpace(cfg.QwenTTSModelTier)
			if tier == "" {
				tier = managedqwen.DefaultModelTier
			}
			return "native " + tier
		}
		model := strings.TrimSpace(cfg.QwenTTSModel)
		if managedqwen.UseManaged(cfg.QwenTTSBinary, model) {
			return "native package (not installed)"
		}
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
		return "managed worker"
	}
	if qwenManagedNativeReady(cfg) {
		return "native worker"
	}
	if managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		return "native worker (not installed)"
	}
	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	return filepath.Base(filepath.Clean(binary))
}

// qwenManagedNativeReady is true when config is the managed (empty path) Qwen
// selection and a native package is installed under models_dir.
func qwenManagedNativeReady(cfg *config.Config) bool {
	if cfg == nil || !managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		return false
	}
	return managedqwen.NativeInstalled(config.ModelsDirFrom(cfg))
}

// ttsBadgeLabel is used on the launcher and conversation header so the active
// TTS engine remains visible outside Settings.
func ttsBadgeLabel(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return "tts " + activeTTSProvider(cfg) + " · " + ttsModelLabel(cfg) + " · " + ttsVoiceModeLabel(cfg) + " · " + ttsEffectiveVoiceLabel(cfg)
}

func ttsProviderDetail(spec tts.ProviderSpec, cfg *config.Config) string {
	switch strings.ToLower(strings.TrimSpace(spec.Name)) {
	case "kokoro":
		return "managed model · static voices · " + ttsEffectiveVoiceLabelForProvider("kokoro", cfg)
	case "qwen3-tts":
		return "model " + ttsModelLabelForProvider("qwen3-tts", cfg) + " · " + ttsBinaryLabel(cfg) + " · " + ttsVoiceModeLabelForProvider("qwen3-tts", cfg) + " · " + ttsEffectiveVoiceLabelForProvider("qwen3-tts", cfg)
	default:
		return spec.Description
	}
}

func ttsVoiceModeLabel(cfg *config.Config) string {
	return ttsVoiceModeLabelForProvider(activeTTSProvider(cfg), cfg)
}

func ttsVoiceModeLabelForProvider(provider string, cfg *config.Config) string {
	if strings.EqualFold(strings.TrimSpace(provider), "qwen3-tts") {
		if cfg == nil || strings.TrimSpace(cfg.QwenTTSMode) == "" {
			if cfg != nil && managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
				return "mode customvoice"
			}
			return "mode unverified/default"
		}
		return "mode " + strings.TrimSpace(cfg.QwenTTSMode)
	}
	return "mode static"
}

func ttsEffectiveVoiceLabel(cfg *config.Config) string {
	return ttsEffectiveVoiceLabelForProvider(activeTTSProvider(cfg), cfg)
}

func ttsEffectiveVoiceLabelForProvider(provider string, cfg *config.Config) string {
	if strings.EqualFold(strings.TrimSpace(provider), "qwen3-tts") {
		if cfg != nil && strings.TrimSpace(cfg.QwenTTSVoice) != "" {
			return "voice " + strings.TrimSpace(cfg.QwenTTSVoice)
		}
		if cfg != nil && managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
			return "voice Vivian"
		}
		return "voice model-native default"
	}
	voice := "af_heart"
	if cfg != nil && strings.TrimSpace(cfg.TTSVoice) != "" {
		voice = strings.TrimSpace(cfg.TTSVoice)
	}
	return "voice " + voice
}

func ttsVoiceSelectionStatus(cfg *config.Config) string {
	if strings.EqualFold(activeTTSProvider(cfg), "qwen3-tts") {
		if qwenManagedNativeReady(cfg) {
			return "Native package has no browsable presets on disk; open Qwen tab or re-run ensure"
		}
		if cfg == nil || managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
			return "Qwen preset voices are not installed; open Settings → Qwen or TTS and install"
		}
		return "Qwen voice controls are not verified by this worker; leave qwen_tts_mode/voice/language/instruction/reference settings empty and use the model-native default."
	}
	return "No browsable voices for the active TTS provider."
}
