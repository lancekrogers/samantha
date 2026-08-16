package stt

import "testing"

func TestNormalizeTranscriptDropsNonSpeechTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"blank audio", "[BLANK_AUDIO]", ""},
		{"music", "[MUSIC]", ""},
		{"bell", "[Bell]", ""},
		{"parens applause", "(applause)", ""},
		{"asterisk buzzer", "*buzzer*", ""},
		{"spaced tag", "[ Music ]", ""},
		{"multiple tags", "[MUSIC] [Bell]", ""},
		{"tag around speech", "[Music] git status", "git status"},
		{"speech then tag", "open the door (creak)", "open the door"},
		{"plain speech untouched", "what time is it", "what time is it"},
		// Truncated sound-event tags (streaming partial / unclosed final).
		// These used to survive normalize and start spurious turns — e.g. "[Buzz".
		{"incomplete buzz bracket", "[Buzz", ""},
		{"incomplete blank audio", "[BLANK_AUDIO", ""},
		{"incomplete parens", "(creak", ""},
		{"speech then incomplete buzz", "open the door [Buzz", "open the door"},
		{"complete buzz still drops", "[Buzz]", ""},
		// Asterisk tags still need a close; do not strip math-like speech.
		{"asterisk speech untouched", "multiply * 2 please", "multiply * 2 please"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTranscript(tt.in); got != tt.want {
				t.Fatalf("normalizeTranscript(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
