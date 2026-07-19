package meeting

import "testing"

func TestStopPhraseSetMatchesExactNormalizedOnly(t *testing.T) {
	set := StopPhraseSet([]string{"That's a wrap"})
	cases := map[string]bool{
		"Stop recording.":              true,
		"stop recording":               true,
		"end meeting":                  true,
		"that's a wrap":                true,
		"please stop recording":        false,
		"we should stop recording now": false,
		"":                             false,
	}
	for phrase, want := range cases {
		if got := set[NormalizeStopPhrase(phrase)]; got != want {
			t.Errorf("NormalizeStopPhrase(%q) in set = %v, want %v", phrase, got, want)
		}
	}
}
