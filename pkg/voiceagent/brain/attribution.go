package brain

import "strings"

// SpeakerNames resolves a stable speaker id (speaker-1) to a display label for
// model prompts. Implementations may return the id itself when no rename is set.
// A nil names map means ids are shown as-is.
type SpeakerNames interface {
	Display(id string) string
}

// UserSpeakerLabel returns the left-hand role used for a user turn in prompts.
// Empty speaker id → "User". With a rename, the human name alone is preferred
// so the model addresses people naturally; without one, the stable id is used.
func UserSpeakerLabel(speakerID string, names SpeakerNames) string {
	speakerID = strings.TrimSpace(speakerID)
	if speakerID == "" {
		return "User"
	}
	if names != nil {
		if d := strings.TrimSpace(names.Display(speakerID)); d != "" {
			return d
		}
	}
	return speakerID
}

// FormatUserLine builds "Label: content" for a user turn.
func FormatUserLine(speakerID, content string, names SpeakerNames) string {
	return UserSpeakerLabel(speakerID, names) + ": " + content
}

// AttachSpeakerNames wires a rename table onto providers that support it.
// No-ops for fakes and providers without attribution.
func AttachSpeakerNames(p Provider, names SpeakerNames) {
	type setter interface {
		SetSpeakerNames(SpeakerNames)
	}
	if s, ok := p.(setter); ok {
		s.SetSpeakerNames(names)
	}
}

// promptUserLine formats one history turn for Claude/Grok flatten prompts.
func promptUserLine(t Turn, agentName string, names SpeakerNames) string {
	switch t.Role {
	case "samantha", "assistant":
		if agentName == "" {
			agentName = "Samantha"
		}
		return agentName + ": " + t.Content
	default:
		return FormatUserLine(t.Speaker, t.Content, names)
	}
}
