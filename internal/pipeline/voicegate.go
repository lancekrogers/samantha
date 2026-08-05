package pipeline

import (
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/events"
)

// toolMarkerRE opens a suppressed tool region: transcript narration ("Called
// the X tool", "Result of calling the X tool:") and bare tool-call JSON. Local
// models roleplay tool exchanges in plain prose (WI-dc9e33 B4 — the field
// capture was fabricated narration, not a real tool call), so this is the
// load-bearing filter; genuine tool calls already ride typed channels.
//
// tests/duet/report.go keeps its own detector on purpose: the harness is the
// measuring instrument, and sharing this one would let a broken gate hide its
// own leaks.
var toolMarkerRE = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bcalled the \S+ tool`),
	regexp.MustCompile(`(?i)\bresult of calling the\b`),
	regexp.MustCompile(`\{\s*"(command|tool|name|input|arguments)"\s*:`),
}

// toolPayloadRE marks lines that are plainly command output rather than
// speech: file:line citations, exit statuses, and output banners.
var toolPayloadRE = regexp.MustCompile(`(?i)(:\d+[:\s]|exited with code|^(final )?output\s*:)`)

// toolRegionLineCap bounds a suppressed tool region. Prose resumption is the
// normal exit; this is the backstop so a model that never returns to
// conversational text cannot mute the rest of the turn.
const toolRegionLineCap = 50

// voiceGate decides what may be spoken. It is a turn-scoped state machine, not
// a line matcher: a tool transcript or a fenced block is a *region* whose
// payload lines carry no marker of their own, and a region routinely spans
// several streamed sentence segments. Suppressing only the marker line — the
// first cut at this — left the payload ("Process exited with code 0", raw
// command output, the body of a fenced block) still being read aloud.
//
// It runs before brain.CleanForVoice, which strips the ``` markers a fenced
// region is recognized by.
type voiceGate struct {
	inFence   bool
	inTool    bool
	toolLines int
}

// filter returns the speakable part of one segment and the number of lines
// suppressed. Call it for every segment of a turn, in order, including ones
// that will not be spoken: the region state has to see the boundaries.
func (g *voiceGate) filter(text string) (string, int) {
	if text == "" {
		return "", 0
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	dropped := 0
	for _, line := range lines {
		if g.suppress(line) {
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

// suppress advances the region state for one line and reports whether it must
// stay out of the voice channel.
func (g *voiceGate) suppress(line string) bool {
	// Fences toggle on the marker itself; an odd count opens or closes a
	// region, an even count is an inline span. Either way the line is not
	// spoken, so a one-line ``` snippet ``` cannot leak its body.
	if fences := strings.Count(line, "```"); fences > 0 {
		if fences%2 == 1 {
			g.inFence = !g.inFence
		}
		return true
	}
	if g.inFence {
		return true
	}

	if g.inTool {
		g.toolLines++
		if g.toolLines > toolRegionLineCap {
			g.endToolRegion()
			return true
		}
		if isSpeakableProse(line) {
			g.endToolRegion()
			return false
		}
		return true
	}

	for _, re := range toolMarkerRE {
		if re.MatchString(line) {
			g.inTool = true
			g.toolLines = 0
			return true
		}
	}
	return false
}

func (g *voiceGate) endToolRegion() {
	g.inTool = false
	g.toolLines = 0
}

// isSpeakableProse reports whether a line inside a tool region looks like the
// model returning to conversation, which closes the region. Deliberately
// strict: default-deny means an ambiguous line stays unspoken, and the cost of
// guessing wrong is only that one sentence is silent.
func isSpeakableProse(line string) bool {
	if line != strings.TrimLeft(line, " \t") {
		return false // indented: still formatted output
	}
	s := strings.TrimSpace(line)
	if s == "" || strings.ContainsAny(s, "{}`") || toolPayloadRE.MatchString(s) {
		return false
	}
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		return false
	}
	for _, re := range toolMarkerRE {
		if re.MatchString(s) {
			return false
		}
	}
	return len(strings.Fields(s)) >= 4
}

// recordStrips accounts for suppressed lines: the count lands in the turn
// metrics and on the bus, so a leak is visible in the activity feed and the
// transcript log even though it was never spoken.
func (p *Pipeline) recordStrips(stripped int, metrics *turnMetrics) {
	if stripped == 0 {
		return
	}
	metrics.toolLeakLines += stripped
	p.emit(events.VoiceGateStripped{Lines: stripped})
}

// gateForSpeech applies a fresh gate to a complete reply — the text-mode path,
// where the whole response arrives as one blob rather than as segments.
func (p *Pipeline) gateForSpeech(text string, metrics *turnMetrics) string {
	g := &voiceGate{}
	speakable, stripped := g.filter(text)
	p.recordStrips(stripped, metrics)
	return speakable
}
