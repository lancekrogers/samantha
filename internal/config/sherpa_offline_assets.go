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

// sherpaWhisperArchiveSHA256 pins the compressed-archive checksum for every
// known model (sizes cross-checked against the release asset metadata). Names
// outside the known list still download via the same URL pattern but without
// checksum enforcement — there is nothing shipped to pin them to.
var sherpaWhisperArchiveSHA256 = map[string]string{
	"tiny":      "c46116994e539aa165266d96b325252728429c12535eb9d8b6a2b10f129e66b1",
	"tiny.en":   "2bd6cf965c8bb3e068ef9fa2191387ee63a9dfa2a4e37582a8109641c20005dd",
	"base":      "911b2083efd7c0dca2ac3b358b75222660dc09fb716d64fbfc417ba6c99ff3de",
	"base.en":   "475bc7052ce299c007f6d5d5407ba8601f819a2867f6eecee510ed17df581542",
	"small":     "486a46afbb7ba798507190ffe02fea2dd726049af212e774537efac6afb210a6",
	"small.en":  "0cdba2b8aaab69e04847f3427cc9709574112e67913a1a84b7fec3a8729faa9a",
	"medium":    "614b1172557049069d846c29d9399640bce83a4dd6c580decebd9ce2a4f32c33",
	"medium.en": "73d95c169a410b5f23a79f8901374b26e0a16a09ea7f02b5e1db983f4cdfdd67",
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
