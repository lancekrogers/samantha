package config

import (
	"fmt"
	"strings"

	"github.com/lancekrogers/samantha/internal/qwen"
)

// ManagedTTS reports whether cfg selects a TTS provider whose model assets
// samantha manages (downloads, verifies, and diagnoses). It is the single
// authority the asset resolver, installer, and doctor consult, so they cannot
// drift when a provider or alias is added.
func ManagedTTS(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.TTSProvider)) {
	case "kokoro", qwen.ProviderName:
		return true
	default:
		return false
	}
}

// ValidateQwenTTSConfig rejects malformed or unsupported Qwen controls before
// a worker is started. Model-specific capability validation still happens in
// the provider; this validates the managed CustomVoice product path and keeps
// reference-audio modes gated until their worker support lands.
func ValidateQwenTTSConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	if !qwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		var unsupported []string
		if mode := strings.TrimSpace(cfg.QwenTTSMode); mode != "" && !strings.EqualFold(mode, "static") {
			unsupported = append(unsupported, "qwen_tts_mode")
		}
		if voice := strings.TrimSpace(cfg.QwenTTSVoice); voice != "" && !strings.EqualFold(voice, "default") {
			unsupported = append(unsupported, "qwen_tts_voice")
		}
		if strings.TrimSpace(cfg.QwenTTSLanguage) != "" {
			unsupported = append(unsupported, "qwen_tts_language")
		}
		if strings.TrimSpace(cfg.QwenTTSInstruction) != "" {
			unsupported = append(unsupported, "qwen_tts_instruction")
		}
		if strings.TrimSpace(cfg.QwenTTSReferenceAudio) != "" {
			unsupported = append(unsupported, "qwen_tts_reference_audio")
		}
		if strings.TrimSpace(cfg.QwenTTSReferenceText) != "" {
			unsupported = append(unsupported, "qwen_tts_reference_text")
		}
		if len(unsupported) > 0 {
			return fmt.Errorf("external Qwen3-TTS worker supports only model-native default/static synthesis; clear unsupported settings: %s", strings.Join(unsupported, ", "))
		}
		return nil
	}
	var unsupported []string
	mode := strings.ToLower(strings.TrimSpace(cfg.QwenTTSMode))
	if mode != "" && mode != "static" && mode != "customvoice" {
		unsupported = append(unsupported, "qwen_tts_mode")
	}
	if voice := strings.TrimSpace(cfg.QwenTTSVoice); voice != "" && !strings.EqualFold(voice, "default") {
		if _, found := qwen.CanonicalVoice(voice); !found {
			return fmt.Errorf("unsupported qwen_tts_voice %q for the managed CustomVoice model", voice)
		}
	}
	if language := strings.TrimSpace(cfg.QwenTTSLanguage); language != "" {
		if _, found := qwen.CanonicalLanguage(language); !found {
			return fmt.Errorf("unsupported qwen_tts_language %q", language)
		}
	}
	// The pinned 0.6B CustomVoice model provides preset timbres but not the
	// instruction-control feature of the optional 1.7B tier.
	if strings.TrimSpace(cfg.QwenTTSInstruction) != "" {
		unsupported = append(unsupported, "qwen_tts_instruction")
	}
	if strings.TrimSpace(cfg.QwenTTSReferenceAudio) != "" {
		unsupported = append(unsupported, "qwen_tts_reference_audio")
	}
	if strings.TrimSpace(cfg.QwenTTSReferenceText) != "" {
		unsupported = append(unsupported, "qwen_tts_reference_text")
	}
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf("managed Qwen3-TTS currently supports preset CustomVoice synthesis; clear unsupported settings: %s", strings.Join(unsupported, ", "))
}
