package anim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func testStyles() Styles {
	return Styles{
		Tip:     lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		Mid:     lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		Core:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Muted:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Label:   lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		Accent:  lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		Hearing: lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		Speak:   lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
		Think:   lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		Border:  lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
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
		t.Fatalf("low and high levels should differ")
	}
}

func TestStage_IdleEmpty(t *testing.T) {
	if Stage(ModeIdle, 0, 0, 40, "", testStyles(), false) != "" {
		t.Fatal("idle stage must be empty")
	}
}

func TestStage_NoFlameArt(t *testing.T) {
	// Regression: stage must stay an EQ strip, not festival flame glyphs.
	s := testStyles()
	for _, mode := range []Mode{ModeListening, ModeHearing, ModeSpeaking} {
		out := Stage(mode, 3, 0.8, 60, "", s, false)
		for _, banned := range []string{")####", ")|||", "`####", "flame"} {
			if strings.Contains(out, banned) {
				t.Fatalf("mode %d still has flame-like art %q in:\n%s", mode, banned, out)
			}
		}
		if out == "" {
			t.Fatalf("mode %d produced empty stage", mode)
		}
	}
}

func TestStage_ReducedMotionStable(t *testing.T) {
	s := testStyles()
	a := Stage(ModeSpeaking, 0, 0.5, 60, "Speaking", s, true)
	b := Stage(ModeSpeaking, 5, 0.5, 60, "Speaking", s, true)
	if a != b {
		t.Fatalf("reduced-motion stages must match across frames")
	}
}

func TestCompactMeter_Modes(t *testing.T) {
	s := testStyles()
	for _, mode := range []Mode{ModeListening, ModeHearing, ModeSpeaking, ModeError} {
		if CompactMeter(mode, 2, 0.6, "", s, false) == "" {
			t.Fatalf("mode %d empty compact meter", mode)
		}
	}
	if CompactMeter(ModeIdle, 0, 0, "", s, false) != "" {
		t.Fatal("idle compact meter must be empty")
	}
}

func TestEffectiveLevel(t *testing.T) {
	if effectiveLevel(ModeListening, 0, 0) <= 0 {
		t.Fatal("listening needs ambient level")
	}
	if effectiveLevel(ModeHearing, 0.8, 0) != 0.8 {
		t.Fatal("hearing should pass through strong levels")
	}
}
