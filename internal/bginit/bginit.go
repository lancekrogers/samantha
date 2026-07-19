// Package bginit seeds lipgloss's background-color cache before bubbletea can
// issue OSC/DSR terminal queries that hang or mis-detect on bare PTYs (VHS,
// CI, script). Import from cmd/samantha with a blank import before the tui
// package runs.
//
// Mirrors obey-installer/camp bginit.
package bginit

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func init() {
	lipgloss.SetHasDarkBackground(backgroundIsDark(os.Getenv("COLORFGBG")))
}

func backgroundIsDark(colorFGBG string) bool {
	if !strings.Contains(colorFGBG, ";") {
		// Prefer dark — matches termcast/VHS theme and Samantha's palette.
		return true
	}
	fields := strings.Split(colorFGBG, ";")
	bg, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return true
	}
	// xterm COLORFGBG: 0–8 are dark-ish; 7 is white.
	return bg >= 0 && bg <= 8 && bg != 7
}
