package brain

import (
	"regexp"
	"strings"
	"unicode"
)

// The flatten prompts and speaker attribution teach the model a "Name: content"
// convention (promptUserLine, FormatUserLine). Models mirror it and open
// replies with the agent's own label — "Samantha: Hi" — which no cleaning
// stage removed, so TTS spoke the name aloud and the labeled reply was echoed
// back into history, reinforcing the habit.
//
// These helpers remove exactly that echo: one leading agent-name label,
// optionally emphasis-wrapped, at the start of a reply. The agent's name
// appearing anywhere else is content and survives — including a reply that
// merely begins with the name without a colon ("Samantha is my name").

// agentLabelRE matches a leading "Name:" label for the given agent name.
// Empty names fall back to "Samantha", mirroring promptUserLine's default so
// the strip and the prompt convention can never disagree about the name.
func agentLabelRE(agentName string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)^\s*(?:\*\*|__|[*_])?` + regexp.QuoteMeta(effectiveAgentName(agentName)) + `(?:\*\*|__|[*_])?\s*:\s*`)
}

func effectiveAgentName(agentName string) string {
	if name := strings.TrimSpace(agentName); name != "" {
		return name
	}
	return "Samantha"
}

// StripAgentLabel removes one leading "AgentName:" label from a complete
// reply. Apply before cleanForVoice, while any markdown emphasis around the
// name is still present, and before the reply is stored in history.
func StripAgentLabel(agentName, text string) string {
	if loc := agentLabelRE(agentName).FindStringIndex(text); loc != nil {
		return text[loc[1]:]
	}
	return text
}

// labelStripper strips the label from a streamed reply without re-chunking
// anything past the head. Output is held only while the reply's opening runes
// could still become the label — at most the name plus wrappers, typically a
// single token — so time-to-first-audio is unaffected: sentence segmentation
// already buffers far longer than this.
type labelStripper struct {
	re      *regexp.Regexp
	lname   string
	pending strings.Builder
	decided bool
}

func newLabelStripper(agentName string) *labelStripper {
	name := effectiveAgentName(agentName)
	return &labelStripper{
		re:    agentLabelRE(name),
		lname: strings.ToLower(name),
	}
}

// Feed accepts the next raw chunk and returns whatever is safe to emit now —
// "" while the head is still ambiguous, the (possibly stripped) held text at
// the moment of decision, and each chunk verbatim thereafter.
func (s *labelStripper) Feed(chunk string) string {
	if s.decided {
		return chunk
	}
	s.pending.WriteString(chunk)
	text := s.pending.String()
	if s.couldBeLabel(text) {
		return ""
	}
	return s.release(text)
}

// Flush releases whatever the head still holds. Call when the stream ends —
// including on error, where the held text is real partial reply — so a short
// reply is never swallowed.
func (s *labelStripper) Flush() string {
	if s.decided {
		return ""
	}
	return s.release(s.pending.String())
}

func (s *labelStripper) release(text string) string {
	s.decided = true
	s.pending.Reset()
	if loc := s.re.FindStringIndex(text); loc != nil {
		return text[loc[1]:]
	}
	return text
}

// couldBeLabel reports whether text is still a strict prefix of some form the
// label can take: whitespace, an emphasis wrapper, the name, closing emphasis
// and spacing, then a colon. The first rune that breaks the pattern resolves
// the head, so an ordinary reply is held for at most one delta.
func (s *labelStripper) couldBeLabel(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return true
	}
	bare := strings.TrimLeft(trimmed, "*_")
	if bare == "" {
		return true
	}
	lower := strings.ToLower(bare)
	if len(lower) < len(s.lname) {
		return strings.HasPrefix(s.lname, lower)
	}
	if !strings.HasPrefix(lower, s.lname) {
		return false
	}
	rest := strings.TrimLeftFunc(strings.TrimLeft(lower[len(s.lname):], "*_"), unicode.IsSpace)
	// Only an unfinished tail keeps the hold; a colon resolves through release,
	// where the regex strips it.
	return rest == ""
}
