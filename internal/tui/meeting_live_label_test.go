package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/speaker"
)

func TestMeetingLiveSpeakerLabelsUtterance(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{
		Status:    speaker.LiveHealthy,
		LastLabel: "speaker-2",
	}}
	m.liveSpeaker = fake
	m.liveStatsKnown = true
	m.liveStats = fake.stats

	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "hello from the second voice",
		At:   time.Now(),
	}))
	if m.lines[0].label != "speaker-2" {
		t.Fatalf("label = %q, want speaker-2 from live stats", m.lines[0].label)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "🎤") {
		t.Fatalf("labeled live line should not show mic:\n%s", view)
	}
	if !strings.Contains(view, "speaker-2") {
		t.Fatalf("missing speaker-2:\n%s", view)
	}
}

func TestMeetingLiveSpeakerStatsReviseLastLine(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{Status: speaker.LiveHealthy}}
	m.liveSpeaker = fake
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{Text: "waiting for id", At: time.Now()}))
	if m.lines[0].label != "" {
		t.Fatalf("expected empty until stats land, got %q", m.lines[0].label)
	}
	fake.stats.LastLabel = "speaker-1"
	updated, cmd := m.Update(liveSpeakerStatsMsg{stats: fake.stats})
	mm := updated.(meetingModel)
	if mm.lines[0].label != "speaker-1" {
		t.Fatalf("label after stats = %q", mm.lines[0].label)
	}
	// Poll should re-arm while recording.
	if cmd == nil {
		t.Fatal("expected liveSpeakerStatsCmd to re-arm")
	}
}

func TestMeetingLiveSpeakerDisabledKeepsMic(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	// No liveSpeaker controller → mic glyph.
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{Text: "solo note", At: time.Now()}))
	view := stripANSI(m.View())
	if !strings.Contains(view, "🎤") {
		t.Fatalf("expected mic without live engine:\n%s", view)
	}
	_ = tea.Msg(nil)
}
