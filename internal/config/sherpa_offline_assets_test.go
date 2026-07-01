package config

import "testing"

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
