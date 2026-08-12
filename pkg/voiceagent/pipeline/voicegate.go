package pipeline

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// toolRegionRE opens a suppressed region: transcript narration announcing a
// tool call or its result. Local models roleplay tool exchanges in plain prose
// (WI-dc9e33 B4 — the field capture was fabricated narration, not a real tool
// call); genuine tool calls already ride typed channels.
//
// This list is deliberately short. Enumerating every phrasing a model might
// invent is a losing game — "Tool result:" and "Running:" slipped past an
// earlier draft — so the load-bearing rule is notSpeechRE below, which admits
// only lines that read as speech. These markers exist to suppress the *payload
// following* an announcement, which on its own may look like anything.
//
// tests/duet/report.go keeps its own detector on purpose: the harness is the
// measuring instrument, and sharing this one would let a broken gate hide its
// own leaks.
var toolRegionRE = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bcalled the \S+ tool`),
	regexp.MustCompile(`(?i)\bresult of calling the\b`),
}

// notSpeechRE marks a line as something other than conversation. This is the
// default-deny rule from L1 inverted into practice: rather than listing tool
// syntax to remove, list what disqualifies a line from ever being spoken —
// code punctuation, file:line citations, colon-delimited records, KEY=value
// pairs, URLs, multi-segment absolute paths, exit statuses, and output banners.
//
// The char class deliberately omits $, <>, and backticks: money, comparisons,
// and inline code are normal voice-agent dialogue. Colon-digit runs require a
// letter-led field (root:x:0:0), not bare H:M:S times. Absolute paths need at
// least two segments so "in /tmp for now" still speaks.
var notSpeechRE = regexp.MustCompile(`(?i)([{}|\\]` +
	`|\S+\.\w+:\d+` + // brain.go:269
	`|[[:alpha:]][^\s]*(:\d+){2,}` + // root:x:0:0 — not 1:23:45
	`|\b\w+=\S` + // AWS_SECRET_KEY=abc
	`|://` +
	`|(^|\s)/\w+(?:/\w+)+` + // /etc/passwd — not lone /tmp
	`|exited with code` +
	`|^(final\s+)?output\s*:` +
	`|^tool\s+result\s*:` +
	`|^running\s*:)`)

// regionLineCap bounds any suppressed region. Prose resumption is the normal
// exit; this is the backstop so a tool transcript that never returns to
// conversation — or a fenced block the model never closes — cannot mute the
// rest of the turn. Silence is a worse failure than a stray line.
const regionLineCap = 50

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
	inFence     bool
	inTool      bool
	regionLines int
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
		g.regionLines = 0
		return true
	}

	// Inside a region, suppress until the model plainly returns to
	// conversation — or until the cap, so an unterminated region (a fence the
	// model never closed, output that never ends) cannot silence the turn.
	if g.inFence || g.inTool {
		g.regionLines++
		capped := g.regionLines > regionLineCap
		if !capped && !isSpeakableProse(line) {
			return true
		}
		g.inFence, g.inTool, g.regionLines = false, false, 0
		if capped {
			return true // the line that hit the cap is still unspoken
		}
	}

	for _, re := range toolRegionRE {
		if re.MatchString(line) {
			g.inTool = true
			g.regionLines = 0
			return true
		}
	}

	// Default deny (L1): outside any region, a line is spoken only if it reads
	// as speech. This is what stops payload that follows an early region exit,
	// and narration phrasings no marker list anticipated.
	return !looksLikeSpeech(line)
}

// looksLikeSpeech reports whether a line is something Samantha could say out
// loud. Deny-shaped rather than allow-shaped so ordinary conversation — short
// interjections included — passes untouched, while anything carrying the marks
// of machine output does not.
func looksLikeSpeech(line string) bool {
	if line != strings.TrimLeft(line, " \t") {
		return false // indented: formatted output, not speech
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return true // blank lines are inert
	}
	if notSpeechRE.MatchString(s) {
		return false
	}
	return strings.ContainsFunc(s, unicode.IsLetter)
}

// isSpeakableProse reports whether a line inside a region looks like the model
// returning to conversation, which closes the region. Stricter than
// looksLikeSpeech — a full sentence, not a fragment — because ending a region
// early is how payload leaks.
func isSpeakableProse(line string) bool {
	if !looksLikeSpeech(line) {
		return false
	}
	s := strings.TrimSpace(line)
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		return false
	}
	for _, re := range toolRegionRE {
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
