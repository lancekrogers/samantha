package config

import (
	"fmt"
	"strings"
)

// sherpaWhisperKnownModels are the sherpa-onnx Whisper release names verified to
// exist upstream; they are documented in the error for unsafe names. Other
// names (large-v3, turbo, distil-medium.en, ...) are published at the same URL
// pattern and pass through, so previously-working configs keep working.
var sherpaWhisperKnownModels = []string{
	"tiny", "tiny.en", "base", "base.en", "small", "small.en", "medium", "medium.en",
}

// SherpaOfflineWhisperModel normalizes the sherpa-onnx Whisper model name used
// in release archive URLs and on-disk file names. Names are restricted to a
// URL-safe charset so an arbitrary config string cannot smuggle path or query
// syntax into the download URL, but any safe name is accepted — the upstream
// project publishes more models than the known list (large-v3, turbo,
// distil-medium.en, ...), and configs that used them must keep working.
func SherpaOfflineWhisperModel(name string) (string, error) {
	model := strings.TrimSpace(strings.ToLower(name))
	if model == "" {
		return "small", nil
	}
	if !safeModelName(model) {
		return "", fmt.Errorf(
			"invalid sherpa offline whisper model %q: use letters, digits, '.', '-' or '_' (known models: %s)",
			name, strings.Join(sherpaWhisperKnownModels, ", "))
	}
	return model, nil
}

// safeModelName reports whether s is safe to interpolate into a release URL and
// file paths: ASCII letters, digits, dot, dash, underscore — and no ".."
// sequence that could act as a path segment.
func safeModelName(s string) bool {
	if strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
