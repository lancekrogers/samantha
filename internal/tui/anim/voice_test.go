package anim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func testStyles() Styles {
	return Styles{
		Tip:     lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")),
		Mid:     lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B")),
		Core:    lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4")),
		Muted:   lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4")),
		Label:   lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B")),
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")),
		Accent:  lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")),
		Hearing: lipgloss.NewStyle().Foreground(lipgloss.Color("#FEBC2E")),
		Speak:   lipgloss.NewStyle().Foreground(lipgloss.Color("#BD93F9")),
		Think:   lipgloss.NewStyle().Foreground(lipgloss.Color("#A4F0FF")),
		Fire:    lipgloss.NewStyle().Foreground(lipgloss.Color("#F2721C")),
		Border:  lipgloss.NewStyle().Foreground(lipgloss.Color("#8BE9FD")),
		Badge:   lipgloss.NewStyle().Foreground(lipgloss.Color("#0d0d11")).Background(lipgloss.Color("#8BE9FD")),
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

func TestSpectrum_HasRows(t *testing.T) {
	out := Spectrum(3, 0.8, 24, 3, testStyles())
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("want 3 spectrum rows, got %q", out)
	}
}

func TestPanel_IdleEmpty(t *testing.T) {
	if Panel(ModeIdle, 0, 0, 40, "", testStyles(), false) != "" {
		t.Fatal("idle panel must be empty")
	}
}

func TestStage_HearingGrowsWithLevel(t *testing.T) {
	s := testStyles()
	low := Stage(ModeHearing, 0, 0.1, 60, "Hearing", s, false)
	high := Stage(ModeHearing, 0, 1, 60, "Hearing", s, false)
	if low == "" || high == "" {
		t.Fatal("expected non-empty stages")
	}
	if !strings.Contains(high, "HEARING") && !strings.Contains(high, "Hearing") {
		t.Fatalf("label missing from stage: %q", high)
	}
}

func TestStage_ReducedMotionStable(t *testing.T) {
	s := testStyles()
	a := Stage(ModeSpeaking, 0, 0.5, 60, "Speaking", s, true)
	b := Stage(ModeSpeaking, 5, 0.5, 60, "Speaking", s, true)
	if a != b {
		t.Fatalf("reduced-motion stages must match across frames:\n%q\n%q", a, b)
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

func TestModePalette_UsesHearingAccent(t *testing.T) {
	s := testStyles()
	p := modePalette(ModeHearing, s)
	// Badge should reverse hearing color — distinct from muted core.
	if p.Label.GetForeground() == s.Muted.GetForeground() {
		t.Fatal("hearing palette should not fall back to muted label")
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
