package meeting

import "strings"

// DefaultStopPhrases end a recording when spoken as a full utterance.
// Matching is exact equality after NormalizeStopPhrase — never substring —
// so a meeting that merely mentions stopping does not stop.
var DefaultStopPhrases = []string{"stop recording", "end meeting", "stop listening"}

// NormalizeStopPhrase lowercases, trims, and strips trailing punctuation
// that STT output often carries (mirrors internal/app command normalization).
func NormalizeStopPhrase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimRight(s, ".,!?"))
}

// StopPhraseSet builds the normalized match set from DefaultStopPhrases + extras.
func StopPhraseSet(extra []string) map[string]bool {
	set := make(map[string]bool, len(DefaultStopPhrases)+len(extra))
	for _, p := range DefaultStopPhrases {
		set[NormalizeStopPhrase(p)] = true
	}
	for _, p := range extra {
		if n := NormalizeStopPhrase(p); n != "" {
			set[n] = true
		}
	}
	return set
}
