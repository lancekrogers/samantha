package pipeline

import (
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/events"
)

// voiceLeakRE detects tool-shaped text inside a speech segment: transcript
// narration ("Called the X tool"), bare tool-call JSON, and fence markers.
// Local models roleplay tool exchanges in plain prose (WI-dc9e33 B4 — the
// field capture was fabricated narration, not a real tool call), so this is
// the load-bearing filter; real tool calls already ride typed channels.
//
// tests/duet/report.go keeps its own copy on purpose: the harness is the
// measuring instrument, and sharing the production filter would let a broken
// gate hide its own leaks.
var voiceLeakRE = []*regexp.Regexp{
	regexp.MustCompile(`(?i)called the \S+ tool`),
	regexp.MustCompile(`(?i)result of calling the`),
	regexp.MustCompile(`\{\s*"(command|tool|name|input|arguments)"\s*:`),
	regexp.MustCompile("```"),
}

// sanitizeForSpeech is the voice gate (WI-dc9e33 B4): it drops tool-shaped
// lines from a segment bound for TTS and reports how many were removed. The
// chat transcript always keeps the raw text — only the spoken channel is
// filtered, so a leak is visible on screen but never read aloud.
func sanitizeForSpeech(text string) (string, int) {
	if text == "" {
		return "", 0
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	dropped := 0
	for _, line := range lines {
		if leakLine(line) {
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	if dropped == 0 {
		return text, 0
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), dropped
}

// gateForSpeech applies the voice gate to one speech-bound segment, recording
// stripped lines in the turn metrics and announcing them on the bus so the
// leak is visible in the activity feed and the transcript log.
func (p *Pipeline) gateForSpeech(text string, metrics *turnMetrics) string {
	speakable, leaked := sanitizeForSpeech(text)
	if leaked > 0 {
		metrics.toolLeakLines += leaked
		p.emit(events.VoiceGateStripped{Lines: leaked})
	}
	return speakable
}

func leakLine(line string) bool {
	for _, re := range voiceLeakRE {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}
