package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
)

func TestFormatMeetingUtteranceLineMicWhenUnlabeled(t *testing.T) {
	line := stripANSI(formatMeetingUtteranceLine(time.Date(2026, 8, 5, 12, 4, 5, 0, time.UTC), "", "hello team"))
	if !strings.Contains(line, "🎤") {
		t.Fatalf("expected mic glyph, got %q", line)
	}
	if !strings.Contains(line, "hello team") {
		t.Fatalf("expected body, got %q", line)
	}
	if strings.Contains(line, "speaker-") {
		t.Fatalf("unlabeled line must not invent speaker id: %q", line)
	}
}

func TestFormatMeetingUtteranceLineUsesSpeakerGlyph(t *testing.T) {
	line := stripANSI(formatMeetingUtteranceLine(time.Now(), "speaker-2", "budget decision"))
	if strings.Contains(line, "🎤") {
		t.Fatalf("labeled line should not use mic: %q", line)
	}
	if !strings.Contains(line, "speaker-2") || !strings.Contains(line, "budget decision") {
		t.Fatalf("got %q", line)
	}
}

func TestFormatMeetingUtteranceLineStripsBracketPrefix(t *testing.T) {
	line := stripANSI(formatMeetingUtteranceLine(time.Now(), "", "[speaker-1] welcome everyone"))
	if !strings.Contains(line, "speaker-1") {
		t.Fatalf("expected label from bracket prefix: %q", line)
	}
	if strings.Contains(line, "[speaker-1]") {
		t.Fatalf("bracket prefix should be stripped from body: %q", line)
	}
	if !strings.Contains(line, "welcome everyone") {
		t.Fatalf("got %q", line)
	}
}

func TestMeetingUtteranceStructuredAndLabelRevise(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "plain line",
		At:   time.Now(),
	}))
	if len(m.lines) != 1 || !m.lines[0].utterance {
		t.Fatalf("lines = %+v", m.lines)
	}
	if m.lines[0].label != "" {
		t.Fatalf("label = %q, want empty", m.lines[0].label)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "🎤") {
		t.Fatalf("view missing mic:\n%s", view)
	}
	id := m.lines[0].id
	m.setUtteranceLabel(id, "speaker-3")
	if m.lines[0].label != "speaker-3" {
		t.Fatalf("label not revised: %q", m.lines[0].label)
	}
	view = stripANSI(m.View())
	if strings.Contains(view, "🎤") {
		t.Fatalf("after label, mic should be gone:\n%s", view)
	}
	if !strings.Contains(view, "speaker-3") {
		t.Fatalf("missing speaker-3:\n%s", view)
	}
}

func TestMeetingDemoBracketLabelOnAppend(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "[speaker-2] thanks — launch readiness",
		At:   time.Now(),
	}))
	if m.lines[0].label != "speaker-2" {
		t.Fatalf("label = %q", m.lines[0].label)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "🎤") {
		t.Fatalf("demo labeled line should not show mic:\n%s", view)
	}
}

func TestMeetingSpeakerLabelMsg(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	id := m.appendUtterance(time.Now(), "", "async label soon")
	m, _ = m.handleListenMsg(meetingSpeakerLabelMsg{lineID: id, label: "speaker-1"})
	if m.lines[0].label != "speaker-1" {
		t.Fatalf("label = %q", m.lines[0].label)
	}
	// Also via Update path through meetingChMsg is engine-facing; direct msg ok for unit.
	_ = tea.Msg(nil)
}
