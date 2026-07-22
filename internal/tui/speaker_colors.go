package tui

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func speakerLabelStyle(label string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(speakerColor(label)).Bold(true)
}

func speakerColor(label string) lipgloss.Color {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if number, err := strconv.Atoi(strings.TrimPrefix(normalized, "speaker-")); err == nil && number > 0 {
		return speakerColors[(number-1)%len(speakerColors)]
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(normalized))
	return speakerColors[int(hash.Sum32()%uint32(len(speakerColors)))]
}

// splitSpeakerLabel recognizes the anonymous label prefix used by live/demo
// transcript providers: "[speaker-N] words". Unlabeled speech is returned
// unchanged so ordinary STT remains visually identical.
func splitSpeakerLabel(text string) (label, spoken string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") {
		return "", text
	}
	end := strings.IndexByte(trimmed, ']')
	if end <= 1 {
		return "", text
	}
	label = strings.TrimSpace(trimmed[1:end])
	if !strings.HasPrefix(strings.ToLower(label), "speaker-") {
		return "", text
	}
	return label, strings.TrimSpace(trimmed[end+1:])
}

func renderLiveMeetingUtterance(text string) string {
	label, spoken := splitSpeakerLabel(text)
	if label == "" {
		return normalStyle.Render(text)
	}
	rendered := speakerLabelStyle(label).Render(label + ":")
	if spoken != "" {
		rendered += " " + normalStyle.Render(spoken)
	}
	return rendered
}

func renderSpeakerUserTurn(label, text string) string {
	if strings.TrimSpace(label) == "" {
		return renderUserTurn(text)
	}
	body := speakerLabelStyle(label).Render("› "+label) + "\n" + normalStyle.Render(text)
	bubble := userBubbleStyle.BorderForeground(speakerColor(label))
	return bubble.Render(body)
}
