package stt

import "regexp"

// nonSpeechTag matches Whisper sound-event annotations — [BLANK_AUDIO], [Music],
// (applause), *buzzer* — which label background noise, not spoken words.
var nonSpeechTag = regexp.MustCompile(`[\[(][^\])]*[\])]|\*[^*]*\*`)

// stripNonSpeechTags removes those annotations. A transcript that was nothing
// but annotations collapses to "", which callers treat as silence.
func stripNonSpeechTags(text string) string {
	return nonSpeechTag.ReplaceAllString(text, " ")
}
