package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestColorForceOverridesNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("SAMANTHA_COLOR_PROFILE", "truecolor")
	forceTUIColorProfile()
	out := headerStyle.Render("Samantha")
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("CLICOLOR_FORCE should override NO_COLOR; profile=%v out=%q",
			lipgloss.ColorProfile(), out)
	}
}
