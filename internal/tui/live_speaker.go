package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/samantha/internal/speaker"
)

const liveSpeakerPollInterval = 250 * time.Millisecond

// LiveSpeakerController is intentionally small so the conversation screen
// can control an optional adapter without knowing how speaker analysis is
// implemented. Stats contain no speaker identity by default.
type LiveSpeakerController interface {
	Stats() speaker.LiveStats
	SetEnabled(bool)
}

type liveSpeakerStatsMsg struct {
	stats speaker.LiveStats
}

func liveSpeakerStatsCmd(controller LiveSpeakerController) tea.Cmd {
	if controller == nil {
		return nil
	}
	return tea.Tick(liveSpeakerPollInterval, func(time.Time) tea.Msg {
		return liveSpeakerStatsMsg{stats: controller.Stats()}
	})
}

func liveSpeakerStatusLabel(status speaker.LiveStatus) string {
	switch status {
	case speaker.LiveDisabled:
		return "speakers off"
	case speaker.LiveUnavailable:
		return "speakers unavailable"
	case speaker.LiveRunning:
		return "speakers starting"
	case speaker.LiveDegraded:
		return "speakers degraded"
	case speaker.LiveHealthy:
		return "speakers healthy"
	case speaker.LiveClosed:
		return "speakers closed"
	default:
		return "speakers unknown"
	}
}

func liveSpeakerFooterLabel(stats speaker.LiveStats) string {
	label := liveSpeakerStatusLabel(stats.Status)
	if stats.LastLabel != "" && (stats.Status == speaker.LiveHealthy || stats.Status == speaker.LiveRunning) {
		return label + " · " + stats.LastLabel
	}
	return label
}

func liveSpeakerStatusStyle(status speaker.LiveStatus) lipgloss.Style {
	switch status {
	case speaker.LiveDegraded, speaker.LiveUnavailable:
		return warningStyle
	case speaker.LiveClosed:
		return dimStyle
	case speaker.LiveDisabled:
		return chipMutedStyle
	default:
		return statusStyle
	}
}
