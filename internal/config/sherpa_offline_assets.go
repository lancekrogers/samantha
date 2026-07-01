package config

import (
	"fmt"
	"strings"
)

// SherpaOfflineWhisperModel normalizes the supported sherpa-onnx Whisper model
// names used in release archive URLs. Keeping this list explicit prevents an
// arbitrary config string from being interpolated into a download URL.
func SherpaOfflineWhisperModel(name string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "":
		return "small", nil
	case "tiny", "tiny.en", "base", "base.en", "small", "small.en", "medium", "medium.en":
		return strings.TrimSpace(strings.ToLower(name)), nil
	default:
		return "", fmt.Errorf("unsupported sherpa offline whisper model %q", name)
	}
}
