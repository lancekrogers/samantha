package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() {
	// Bubble Tea / VHS / modern terminals: force a real color profile so
	// ANSI-256 and hex styles don't collapse to monochrome when the
	// runtime mis-detects the emulator (common under recordings).
	if os.Getenv("NO_COLOR") != "" {
		return
	}
	if os.Getenv("CLICOLOR_FORCE") != "" || os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}
	// Default: prefer 256-color over Ascii for interactive TTY sessions.
	if lipgloss.ColorProfile() == termenv.Ascii {
		lipgloss.SetColorProfile(termenv.ANSI256)
	}
}

// Truecolor-friendly palette (reads well on dark themes in VHS + Ghostty).
var (
	colorAccent  = lipgloss.Color("#8BE9FD") // cyan
	colorUser    = lipgloss.Color("#82AAFF") // blue
	colorAgent   = lipgloss.Color("#C3E88D") // green
	colorDim     = lipgloss.Color("#6272A4") // muted
	colorNormal  = lipgloss.Color("#F8F8F2") // near-white
	colorStatus  = lipgloss.Color("#50FA7B") // bright green
	colorError   = lipgloss.Color("#FF5555") // red
	colorSelect  = lipgloss.Color("#FF79C6") // pink
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
