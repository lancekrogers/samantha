package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/samantha/internal/speaker"
)

const (
	liveSpeakerPollInterval = 250 * time.Millisecond
	// liveSpeakerStickyHold keeps the last good speaker-N when the engine
	// briefly reports empty (gap / low confidence). New non-empty labels
	// always win immediately so real turn-taking is not delayed.
	liveSpeakerStickyHold = 3 * time.Second
)

// LiveSpeakerController is intentionally small so the conversation screen
// can control an optional adapter without knowing how speaker analysis is
// implemented. Stats contain no speaker identity by default.
type LiveSpeakerController interface {
	Stats() speaker.LiveStats
	SetEnabled(bool)
}

// stickyLiveLabel remembers the last non-empty speaker-N so short empty gaps
// do not thrash the mic glyph or flip-flop the footer.
type stickyLiveLabel struct {
	label string
	until time.Time
}

// Observe returns the label to display. Non-empty input refreshes the hold;
// empty input returns the sticky value until the hold expires.
func (s *stickyLiveLabel) Observe(label string, now time.Time) string {
	label = strings.TrimSpace(label)
	if label != "" {
		s.label = label
		s.until = now.Add(liveSpeakerStickyHold)
		return label
	}
	if s.label != "" && now.Before(s.until) {
		return s.label
	}
	return ""
}

// Clear drops the hold (session stop / disable).
func (s *stickyLiveLabel) Clear() {
	s.label = ""
	s.until = time.Time{}
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

func renderLiveSpeakerFooter(stats speaker.LiveStats) string {
	rendered := liveSpeakerStatusStyle(stats.Status).Render(liveSpeakerStatusLabel(stats.Status))
	if stats.LastLabel != "" && (stats.Status == speaker.LiveHealthy || stats.Status == speaker.LiveRunning) {
		rendered += dimStyle.Render(" · ") + speakerLabelStyle(stats.LastLabel).Render(stats.LastLabel)
	}
	return rendered
}

func (m *conversationModel) currentLiveSpeakerLabel() string {
	if m.liveSpeaker == nil {
		return ""
	}
	stats := m.liveSpeaker.Stats()
	m.liveSpeakerStats = stats
	m.liveSpeakerStatsKnown = true
	if stats.Status != speaker.LiveHealthy && stats.Status != speaker.LiveRunning {
		return m.stickyLive.Observe("", time.Now())
	}
	return m.stickyLive.Observe(stats.LastLabel, time.Now())
}
