package brain

import (
	"strings"
	"testing"
)

// The tier lists are the contract, so the tests read them rather than repeating
// them: adding a filler to either list extends coverage automatically instead of
// silently going untested.
func TestVoicedFillersReachTTS(t *testing.T) {
	// The regex sources are written for matching; these are what a model
	// actually emits, in the capitalised and punctuated forms it emits them in.
	samples := map[string][]string{
		`hmm+`: {"Hmm, good question", "hmm, good question", "Hmmm, good question", "Well hmm. Let me think"},
		`haha`: {"Haha, that's a good one", "haha that's a good one", "Right, haha. Anyway"},
	}

	for _, filler := range voicedFillers {
		cases, ok := samples[filler]
		if !ok {
			t.Fatalf("voiced filler %q has no samples: a filler promoted to the voiced "+
				"tier must come with the utterances that were listened to", filler)
		}
		for _, in := range cases {
			t.Run(in, func(t *testing.T) {
				got := cleanForVoice(in)
				word := strings.TrimSuffix(filler, "+")
				if !strings.Contains(strings.ToLower(got), word) {
					t.Errorf("cleanForVoice(%q) = %q — %q must reach TTS so Kokoro can voice it",
						in, got, word)
				}
			})
		}
	}
}

func TestStrippedFillersNeverReachTTS(t *testing.T) {
	samples := map[string][]string{
		`umm+`: {"Umm, sure", "It was, umm, complicated", "UMM, sure", "Ummm, sure"},
		`uhh+`: {"Uhh I forgot", "uhh, I forgot"},
		`ahh+`: {"Ahh, I see", "ahh I see"},
		`mmm+`: {"Mmm, interesting", "mmm interesting"},
		`heh`:  {"Heh, fair enough", "heh fair enough"},
	}

	for _, filler := range strippedFillers {
		cases, ok := samples[filler]
		if !ok {
			t.Fatalf("stripped filler %q has no samples", filler)
		}
		word := strings.TrimSuffix(filler, "+")
		for _, in := range cases {
			t.Run(in, func(t *testing.T) {
				got := strings.ToLower(cleanForVoice(in))
				if strings.Contains(got, word) {
					t.Errorf("cleanForVoice(%q) = %q — Kokoro spells %q out letter by letter, "+
						"so it must be removed until a listen check says otherwise", in, got, word)
				}
			})
		}
	}
}

// The two tiers must stay disjoint. This checks BEHAVIOUR, not the equality of
// the regex source strings: adding "hmm" (without the "+") to strippedFillers
// would pass a string-comparison check while silently deleting a voiced filler.
func TestFillerTiersAreDisjoint(t *testing.T) {
	for _, voiced := range voicedFillers {
		sample := strings.TrimSuffix(voiced, "+")
		if fillerRE.MatchString(sample) {
			t.Errorf("the stripped-tier regex matches voiced filler %q; it cannot be both "+
				"spoken and stripped", sample)
		}
	}
	for _, stripped := range strippedFillers {
		sample := strings.TrimSuffix(stripped, "+")
		if voicedFillerRE.MatchString(sample) && fillerRE.MatchString(sample) {
			t.Errorf("filler %q is matched by both tiers", sample)
		}
	}
}

// A reply of nothing but a voiced filler is still a turn that said nothing.
// Before the voiced tier existed, cleaning emptied it and the recovery line took
// over; now it survives cleaning, so emptiness is no longer the right question.
func TestFillerOnlyReplyFallsBackToRecoveryLine(t *testing.T) {
	fillerOnly := []string{"Hmm.", "Hmm", "hmm...", "Haha!", "Hmm, haha.", "  Hmm,  ", "Hmm. Haha!"}
	for _, in := range fillerOnly {
		t.Run(in, func(t *testing.T) {
			if hasSpeakableContent(cleanForVoice(in)) {
				t.Errorf("cleanForVoice(%q) = %q was treated as speakable content; a reply of "+
					"nothing but hesitation must fall back to the recovery line",
					in, cleanForVoice(in))
			}
			if got := spokenOrFallback(cleanForVoice(in)); got != fallbackResponse {
				t.Errorf("spokenOrFallback(%q) = %q, want the recovery line", in, got)
			}
		})
	}

	// The mirror image: a filler with real content after it must survive intact.
	withContent := []string{"Hmm, good question", "Haha, that's fair", "Hmm. Let me check."}
	for _, in := range withContent {
		t.Run(in, func(t *testing.T) {
			cleaned := cleanForVoice(in)
			if got := spokenOrFallback(cleaned); got != cleaned {
				t.Errorf("spokenOrFallback(%q) = %q, want %q — a filler followed by content "+
					"is a real reply", in, got, cleaned)
			}
		})
	}
}

// Emoji are stripped on the cleanForVoice path too, not only at the synthesis
// boundary. That keeps them out of conversation history (the persona bans them,
// and echoing them back teaches the model to keep using them) and makes the
// pipeline's "is there anything to say" check honest for an emoji-only segment.
func TestCleanForVoiceStripsEmoji(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Nice 👍 work", "Nice work"},
		{"Done! 🎉", "Done!"},
		{"👍", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := cleanForVoice(tt.in); got != tt.want {
				t.Errorf("cleanForVoice(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// Numbers must NOT be rewritten here: this output becomes the transcript and
	// the conversation history the model reads next turn.
	const withNumbers = "The answer is 42 in 1999"
	if got := cleanForVoice(withNumbers); got != withNumbers {
		t.Errorf("cleanForVoice(%q) = %q — numbers must reach history as numbers; "+
			"year reading belongs at the synthesis boundary", withNumbers, got)
	}
}

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
		// Voiced tier: Kokoro hums and laughs these, so they pass through
		// untouched — punctuation, capitalisation and all.
		{"leading hmm with comma kept", "Hmm, hello there", "Hmm, hello there"},
		{"elongated hmmm kept", "Hmmm, let me think", "Hmmm, let me think"},
		{"leading haha kept", "Haha that's a good one", "Haha that's a good one"},
		// Stripped tier: removed with the trailing comma.
		{"mid-sentence umm", "It was, umm, complicated", "It was, complicated"},
		{"uppercase filler", "UMM, sure", "sure"},
		{"uhh stripped", "Uhh I forgot", "I forgot"},
		// The stripped tier must not reach inside a voiced one: the "mmm" in
		// "Hmmm" is not a word of its own.
		{"mmm inside hmmm not stripped", "Hmmm.", "Hmmm."},
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
