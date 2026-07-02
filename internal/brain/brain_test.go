package brain

import "testing"

func TestCleanForVoice(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Words containing filler substrings must survive intact.
		{"summer preserved", "I love summer afternoons", "I love summer afternoons"},
		{"dummy preserved", "Use a dummy value for now", "Use a dummy value for now"},
		{"hummingbird preserved", "A hummingbird visited the feeder", "A hummingbird visited the feeder"},
		{"summary preserved", "Here's a summary of the plan", "Here's a summary of the plan"},
		{"filler with word suffix preserved", "The hmms from the crowd grew louder", "The hmms from the crowd grew louder"},
		// Real fillers stripped, including the trailing comma.
		{"leading hmm with comma", "Hmm, hello there", "hello there"},
		{"elongated hmmm", "Hmmm, let me think", "let me think"},
		{"mid-sentence umm", "It was, umm, complicated", "It was, complicated"},
		{"leading haha", "Haha that's a good one", "that's a good one"},
		{"uppercase filler", "UMM, sure", "sure"},
		{"uhh stripped", "Uhh I forgot", "I forgot"},
		// Markdown still stripped.
		{"bold stripped", "**important** point", "important point"},
		{"heading stripped", "# Title here", "Title here"},
		{"code fence stripped", "```code``` here", "code here"},
		{"empty stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanForVoice(tt.in); got != tt.want {
				t.Errorf("cleanForVoice(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
