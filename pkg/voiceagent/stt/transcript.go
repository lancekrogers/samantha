package stt

import "regexp"

// nonSpeechTag matches Whisper sound-event annotations — [BLANK_AUDIO], [Music],
// (applause), *buzzer* — which label background noise, not spoken words.
//
// For bracket/paren tags the closer is optional so a truncated partial like
// "[Buzz" (no ']') still strips. Streaming finals sometimes emit the open half
// of a tag before the model commits to a close; without this, that fragment
// becomes a turn. Asterisk forms still require a closing '*' so ordinary speech
// like "multiply * 2" is not swallowed.
var nonSpeechTag = regexp.MustCompile(`[\[(][^\])]*([\])]|$)|\*[^*]*\*`)

// stripNonSpeechTags removes those annotations. A transcript that was nothing
// but annotations collapses to "", which callers treat as silence.
func stripNonSpeechTags(text string) string {
	return nonSpeechTag.ReplaceAllString(text, " ")
}
