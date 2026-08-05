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

func TestStickyLiveLabelHoldsAcrossEmptyGap(t *testing.T) {
	var s stickyLiveLabel
	now := time.Now()
	if got := s.Observe("speaker-1", now); got != "speaker-1" {
		t.Fatalf("first = %q", got)
	}
	// Empty within hold → still speaker-1.
	if got := s.Observe("", now.Add(time.Second)); got != "speaker-1" {
		t.Fatalf("sticky within hold = %q, want speaker-1", got)
	}
	// New speaker wins immediately (turn-taking, not anti-flip lock).
	if got := s.Observe("speaker-2", now.Add(2*time.Second)); got != "speaker-2" {
		t.Fatalf("new label = %q, want speaker-2", got)
	}
	// After hold expires, empty drops.
	if got := s.Observe("", now.Add(2*time.Second+liveSpeakerStickyHold+time.Millisecond)); got != "" {
		t.Fatalf("after hold = %q, want empty", got)
	}
}

func TestMeetingLiveSpeakerStickyAcrossEmptyStats(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{
		Status:    speaker.LiveHealthy,
		LastLabel: "speaker-1",
	}}
	m.liveSpeaker = fake
	// Seed sticky via stats poll.
	updated, _ := m.Update(liveSpeakerStatsMsg{stats: fake.stats})
	m = updated.(meetingModel)
	// Engine briefly loses the id; utterance should still get sticky label.
	fake.stats.LastLabel = ""
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "still the first voice",
		At:   time.Now(),
	}))
	if m.lines[0].label != "speaker-1" {
		t.Fatalf("label = %q, want sticky speaker-1", m.lines[0].label)
	}
}

func TestMeetingCLIInitWiresLiveSpeakerPoll(t *testing.T) {
	// Avoid full Init (starts listen loop). Assert the same live wiring
	// RunMeeting + Init use: assign field, enable, poll cmd.
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{Status: speaker.LiveHealthy}}
	m := newEmbeddedMeeting()
	opts := MeetingOpts{LiveSpeaker: fake}
	// Mirrors RunMeeting before tea.NewProgram.
	m.opts = opts
	m.liveSpeaker = opts.LiveSpeaker
	m.sessionPhase = meetingSessionRecording

	cmd := m.meetingLiveStartCmd()
	if cmd == nil {
		t.Fatal("meetingLiveStartCmd must return liveSpeakerStatsCmd")
	}
	if len(fake.enabled) == 0 || !fake.enabled[len(fake.enabled)-1] {
		t.Fatal("must SetEnabled(true) on live speaker (CLI path)")
	}
}

func TestMeetingLiveStartCmdNilWhenNoController(t *testing.T) {
	m := newEmbeddedMeeting()
	if cmd := m.meetingLiveStartCmd(); cmd != nil {
		t.Fatal("expected nil without live speaker")
	}
}
