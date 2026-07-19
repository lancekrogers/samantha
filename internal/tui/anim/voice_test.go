package anim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func testStyles() Styles {
	return Styles{
		Tip:    lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")),
		Mid:    lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B")),
		Core:   lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4")),
		Muted:  lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4")),
		Label:  lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B")),
		Error:  lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")),
		Accent: lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")),
	}
}

func TestWaveform_LevelAffectsContent(t *testing.T) {
	s := testStyles()
	low := Waveform(0, 0.05, 16, s)
	high := Waveform(0, 0.95, 16, s)
	if low == "" || high == "" {
		t.Fatal("expected non-empty waveforms")
	}
	if low == high {
		t.Fatalf("low and high levels should differ: low=%q high=%q", low, high)
	}
}

func TestPanel_IdleEmpty(t *testing.T) {
	if Panel(ModeIdle, 0, 0, 40, "", testStyles(), false) != "" {
		t.Fatal("idle panel must be empty")
	}
}

func TestPanel_HearingGrowsWithLevel(t *testing.T) {
	s := testStyles()
	low := Panel(ModeHearing, 0, 0.1, 40, "Hearing", s, false)
	high := Panel(ModeHearing, 0, 1, 40, "Hearing", s, false)
	if strings.Count(low, "\n") > strings.Count(high, "\n") {
		t.Fatalf("low panel should not be taller than high: low lines=%d high lines=%d",
			strings.Count(low, "\n")+1, strings.Count(high, "\n")+1)
	}
	if !strings.Contains(high, "Hearing") {
		t.Fatalf("label missing from panel: %q", high)
	}
}

func TestPanel_ReducedMotionStable(t *testing.T) {
	s := testStyles()
	a := Panel(ModeSpeaking, 0, 0.5, 40, "Speaking", s, true)
	b := Panel(ModeSpeaking, 5, 0.5, 40, "Speaking", s, true)
	if a != b {
		t.Fatalf("reduced-motion panels must match across frames:\n%q\n%q", a, b)
	}
}

func TestCompactMeter_Modes(t *testing.T) {
	s := testStyles()
	for _, mode := range []Mode{ModeListening, ModeHearing, ModeSpeaking, ModeError} {
		out := CompactMeter(mode, 2, 0.6, "", s, false)
		if out == "" {
			t.Fatalf("mode %d produced empty compact meter", mode)
		}
	}
	if CompactMeter(ModeIdle, 0, 0, "", s, false) != "" {
		t.Fatal("idle compact meter must be empty")
	}
}

func TestClampAndEffectiveLevel(t *testing.T) {
	if clamp01(-1) != 0 || clamp01(2) != 1 {
		t.Fatal("clamp01 bounds failed")
	}
	if effectiveLevel(ModeListening, 0, 0) <= 0 {
		t.Fatal("listening should have ambient level")
	}
	if effectiveLevel(ModeHearing, 0.8, 0) != 0.8 {
		t.Fatal("hearing should pass through strong levels")
	}
}
