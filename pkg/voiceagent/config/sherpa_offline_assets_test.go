package config

import (
	"strings"
	"testing"
)

func TestSherpaOfflineWhisperModelNormalizesSupportedModels(t *testing.T) {
	cases := map[string]string{
		"":          "small",
		" SMALL ":   "small",
		"base.en":   "base.en",
		"medium.en": "medium.en",
	}
	for in, want := range cases {
		got, err := SherpaOfflineWhisperModel(in)
		if err != nil {
			t.Fatalf("SherpaOfflineWhisperModel(%q) error = %v", in, err)
		}
		if got != want {
			t.Fatalf("SherpaOfflineWhisperModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSherpaOfflineWhisperModelRejectsUnknownModel(t *testing.T) {
	if _, err := SherpaOfflineWhisperModel("../base.en"); err == nil {
		t.Fatal("SherpaOfflineWhisperModel() error = nil, want unsupported model")
	}
}

// TestSherpaOfflineWhisperModelAcceptsPublishedNonListedModels guards config
// compatibility: sherpa-onnx publishes more whisper archives than the known
// list (large-v3, turbo, distil variants), and configs that used them worked
// before the manifest refactor — they must keep working.
func TestSherpaOfflineWhisperModelAcceptsPublishedNonListedModels(t *testing.T) {
	for _, in := range []string{"large-v3", "turbo", "distil-medium.en", "Medium.EN"} {
		got, err := SherpaOfflineWhisperModel(in)
		if err != nil {
			t.Fatalf("SherpaOfflineWhisperModel(%q) error = %v, want pass-through", in, err)
		}
		if got != strings.ToLower(strings.TrimSpace(in)) {
			t.Fatalf("SherpaOfflineWhisperModel(%q) = %q", in, got)
		}
	}
}

// TestSherpaOfflineWhisperModelRejectsUnsafeNames pins the URL-safety guard and
// requires the error to name the known models so users can self-correct.
func TestSherpaOfflineWhisperModelRejectsUnsafeNames(t *testing.T) {
	for _, in := range []string{"../base.en", "base/../../evil", "small?x=1", "tiny en", "model#frag"} {
		_, err := SherpaOfflineWhisperModel(in)
		if err == nil {
			t.Fatalf("SherpaOfflineWhisperModel(%q) error = nil, want rejection", in)
		}
		if !strings.Contains(err.Error(), "small.en") {
			t.Fatalf("error %q should list the known models", err)
		}
	}
}
