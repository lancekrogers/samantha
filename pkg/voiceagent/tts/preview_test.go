package tts

import "testing"

func TestSpokenPreviewName(t *testing.T) {
	cases := map[string]string{
		"":          "this voice",
		"af_heart":  "Heart",
		"bm_george": "George",
		"af-bella":  "Bella",
		"Uncle_Fu":  "Uncle Fu",
		"Vivian":    "Vivian",
		"Ryan":      "Ryan",
		"serena":    "Serena",
	}
	for in, want := range cases {
		if got := SpokenPreviewName(in); got != want {
			t.Errorf("SpokenPreviewName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpokenPreviewLineUsesVoiceName(t *testing.T) {
	got := SpokenPreviewLine("af_heart")
	if got != "Hi, I'm Heart. This is how I sound." {
		t.Fatalf("line = %q", got)
	}
	if got := SpokenPreviewLine("Vivian"); got != "Hi, I'm Vivian. This is how I sound." {
		t.Fatalf("qwen line = %q", got)
	}
}
