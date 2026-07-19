package tui

import "github.com/charmbracelet/lipgloss"

// Truecolor-friendly palette (reads well on dark themes in VHS + Ghostty).
var (
	colorAccent = lipgloss.Color("#8BE9FD") // cyan
	colorUser   = lipgloss.Color("#82AAFF") // blue
	colorAgent  = lipgloss.Color("#C3E88D") // green
	colorDim    = lipgloss.Color("#6272A4") // muted
	colorNormal = lipgloss.Color("#F8F8F2") // near-white
	colorStatus = lipgloss.Color("#50FA7B") // bright green
	colorError  = lipgloss.Color("#FF5555") // red
	colorSelect = lipgloss.Color("#FF79C6") // pink
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			MarginBottom(1)

	// headerStyle is titleStyle without the margin, for single-line headers.
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorSelect).
			Bold(true)

	normalStyle = lipgloss.NewStyle().
			Foreground(colorNormal)

	dimStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	statusStyle = lipgloss.NewStyle().
			Foreground(colorStatus)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError)

	userStyle = lipgloss.NewStyle().
			Foreground(colorUser).
			Bold(true)

	samanthaStyle = lipgloss.NewStyle().
			Foreground(colorAgent).
			Bold(true)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)
)
