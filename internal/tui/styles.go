package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Brand palette. Hex values paint as truecolor on modern terminals; under
// SAMANTHA_COLOR_PROFILE=ansi (VHS) lipgloss maps them onto bright 16-color
// slots that the termcast theme colors vividly (cyan/amber/purple/green).
var (
	colorBg      = lipgloss.Color("#0d0d11")
	colorRaised  = lipgloss.Color("#16161a")
	colorBorder  = lipgloss.Color("#2a2a33")
	colorAccent  = lipgloss.Color("#8BE9FD") // cyan  → bright cyan
	colorUser    = lipgloss.Color("#57A6FF") // blue  → bright blue
	colorAgent   = lipgloss.Color("#50FA7B") // green → bright green
	colorDim     = lipgloss.Color("#6B7280") // muted gray
	colorNormal  = lipgloss.Color("#F8F8F2") // near-white
	colorStatus  = lipgloss.Color("#50FA7B") // bright green
	colorError   = lipgloss.Color("#FF5F57") // red
	colorSelect  = lipgloss.Color("#FF79C6") // pink  → bright magenta
	colorHearing = lipgloss.Color("#FEBC2E") // amber → bright yellow
	colorSpeak   = lipgloss.Color("#BD93F9") // purple → bright magenta
	colorThink   = lipgloss.Color("#A4F0FF") // ice cyan
)

func init() {
	// Package init runs before Run(); force a real profile early so package-
	// level styles never render under a monochrome detection.
	forceTUIColorProfile()
}

// forceTUIColorProfile ensures lipgloss emits real color sequences even when
// the host terminal (or a recorder like VHS/ttyd) reports a weak capability.
// Mirrors festival: pin dark bg + truecolor so OSC 11 / DSR never leaves us
// stuck on an Ascii profile with zero SGR output.
//
// CLICOLOR_FORCE / SAMANTHA_COLOR_PROFILE override NO_COLOR so demos can still
// paint when the parent agent shell exported NO_COLOR=1.
func forceTUIColorProfile() {
	force := (os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0") ||
		(os.Getenv("FORCE_COLOR") != "" && os.Getenv("FORCE_COLOR") != "0") ||
		os.Getenv("SAMANTHA_COLOR_PROFILE") != ""
	if os.Getenv("NO_COLOR") != "" && !force {
		return
	}
	// Never block on OSC background queries under bare PTYs.
	lipgloss.SetHasDarkBackground(true)

	// Explicit override for demos/CI.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SAMANTHA_COLOR_PROFILE"))) {
	case "truecolor", "24bit", "true":
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	case "ansi256", "256":
		lipgloss.SetColorProfile(termenv.ANSI256)
		return
	case "ansi", "16":
		lipgloss.SetColorProfile(termenv.ANSI)
		return
	case "ascii", "mono":
		lipgloss.SetColorProfile(termenv.Ascii)
		return
	}
	// Prefer truecolor when forced or advertised; otherwise still avoid Ascii.
	if force ||
		os.Getenv("COLORTERM") == "truecolor" ||
		os.Getenv("COLORTERM") == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}
	// Default for interactive TTY: truecolor when the profile looks monochrome.
	if lipgloss.ColorProfile() == termenv.Ascii {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

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

	// warningStyle is amber callout text (limited modes, soft failures).
	warningStyle = lipgloss.NewStyle().
			Foreground(colorHearing)

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

	hearingStyle = lipgloss.NewStyle().
			Foreground(colorHearing).
			Bold(true)

	speakStyle = lipgloss.NewStyle().
			Foreground(colorSpeak).
			Bold(true)

	thinkStyle = lipgloss.NewStyle().
			Foreground(colorThink)

	// headerBarStyle is a full-width brand strip behind the conversation title.
	headerBarStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorNormal).
			Background(colorRaised).
			Padding(0, 1)

	chipStyle = lipgloss.NewStyle().
			Foreground(colorBg).
			Background(colorAccent).
			Bold(true).
			Padding(0, 1)

	chipMutedStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Background(colorRaised).
			Padding(0, 1)

	userBubbleStyle = lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "┃", Top: " ", Bottom: " ", Right: " "}).
			BorderForeground(colorUser).
			Foreground(colorNormal).
			PaddingLeft(1)

	agentBubbleStyle = lipgloss.NewStyle().
				Border(lipgloss.Border{Left: "┃", Top: " ", Bottom: " ", Right: " "}).
				BorderForeground(colorAgent).
				Foreground(colorNormal).
				PaddingLeft(1)
)
