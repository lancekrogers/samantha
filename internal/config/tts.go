package config

import "strings"

// ManagedTTS reports whether cfg selects a TTS provider whose model assets
// samantha manages (downloads, verifies, and diagnoses). It is the single
// authority the asset resolver, installer, and doctor consult, so they cannot
// drift when a provider or alias is added.
func ManagedTTS(cfg *Config) bool {
	return strings.EqualFold(cfg.TTSProvider, "kokoro")
}
