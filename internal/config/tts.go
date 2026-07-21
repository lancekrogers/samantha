package config

import (
	"fmt"
	"strings"
)

// ManagedTTS reports whether cfg selects a TTS provider whose model assets
// samantha manages (downloads, verifies, and diagnoses). It is the single
// authority the asset resolver, installer, and doctor consult, so they cannot
// drift when a provider or alias is added.
func ManagedTTS(cfg *Config) bool {
	return strings.EqualFold(cfg.TTSProvider, "kokoro")
}

// ValidateQwenTTSConfig rejects controls that the currently verified native
// worker cannot honor. Keeping this check in config lets provider creation,
// batch rendering, and doctor report the same actionable failure before a
// sentence is synthesized.
func ValidateQwenTTSConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
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
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf("native Qwen3-TTS worker currently supports only model-native default/static synthesis; clear unsupported settings: %s", strings.Join(unsupported, ", "))
}
