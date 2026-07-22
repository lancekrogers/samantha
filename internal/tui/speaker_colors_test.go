package tui

import (
	"strings"
	"testing"
)

func TestLiveMeetingUtteranceUsesSpeakerPalette(t *testing.T) {
	first := renderLiveMeetingUtterance("[speaker-1] hello there")
	second := renderLiveMeetingUtterance("[speaker-2] good morning")
	if speakerColor("speaker-1") == speakerColor("speaker-2") {
		t.Fatal("live speakers must not share a color")
	}
	if got := stripANSI(first); !strings.Contains(got, "speaker-1: hello there") {
		t.Fatalf("first live turn = %q", got)
	}
	if got := stripANSI(second); !strings.Contains(got, "speaker-2: good morning") {
		t.Fatalf("second live turn = %q", got)
	}
	if got := stripANSI(renderLiveMeetingUtterance("plain speech")); !strings.Contains(got, "plain speech") {
		t.Fatalf("unlabeled speech = %q", got)
	}
}

func TestSpeakerUserTurnColorsHeaderAndBubble(t *testing.T) {
	turn := stripANSI(renderSpeakerUserTurn("speaker-3", "launch update"))
	if !strings.Contains(turn, "› speaker-3") || !strings.Contains(turn, "launch update") {
		t.Fatalf("speaker turn = %q", turn)
	}
	plain := stripANSI(renderSpeakerUserTurn("", "typed message"))
	if !strings.Contains(plain, "› You") {
		t.Fatalf("plain turn = %q", plain)
	}
}

func TestSplitSpeakerLabelRejectsOrdinaryBrackets(t *testing.T) {
	if label, _ := splitSpeakerLabel("[note] follow up"); label != "" {
		t.Fatalf("ordinary bracket parsed as speaker %q", label)
	}
	label, spoken := splitSpeakerLabel(" [Speaker-4]  decision made ")
	if label != "Speaker-4" || spoken != "decision made" {
		t.Fatalf("split = %q, %q", label, spoken)
	}
}
