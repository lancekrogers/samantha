package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

func sizedMeeting(t *testing.T, w, h int) meetingModel {
	t.Helper()
	m := meetingModel{
		opts: MeetingOpts{
			Description: "Standup",
			Path:        "/tmp/standup.log",
		},
		started:   time.Now(),
		voiceMode: anim.ModeListening,
		status:    "Listening",
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	mm := updated.(meetingModel)
	if !mm.ready {
		t.Fatal("not ready after resize")
	}
	return mm
}

func TestMeetingViewShowsDescriptionAndEQ(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	view := m.View()
	for _, want := range []string{"Meeting", "Standup", "listening"} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(want)) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestMeetingPhaseAndLevelUpdateMode(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	m, _ = m.handleListenMsg(meetingPhaseMsg("listening"))
	if m.voiceMode != anim.ModeListening {
		t.Fatalf("mode = %v", m.voiceMode)
	}
	m, _ = m.handleListenMsg(meetingLevelMsg(0.8))
	if m.voiceMode != anim.ModeHearing {
		t.Fatalf("loud level should promote to hearing, got %v", m.voiceMode)
	}
	m, _ = m.handleListenMsg(meetingPhaseMsg("hearing"))
	m, _ = m.handleListenMsg(meetingPartialMsg("hello world"))
	if m.partial != "hello world" {
		t.Fatalf("partial = %q", m.partial)
	}
	if !strings.Contains(m.View(), "hello world") {
		t.Fatal("partial not in view")
	}
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "hello world",
		At:   time.Date(2026, 7, 19, 12, 0, 5, 0, time.UTC),
	}))
	if m.utterances != 1 || m.partial != "" {
		t.Fatalf("utterances=%d partial=%q", m.utterances, m.partial)
	}
	if !strings.Contains(m.View(), "hello world") {
		t.Fatal("final utterance not in view")
	}
	if m.voiceMode != anim.ModeListening {
		t.Fatalf("after final, want listening, got %v", m.voiceMode)
	}
}

func TestMeetingQuitKeyCancels(t *testing.T) {
	cancelled := false
	m := sizedMeeting(t, 80, 24)
	m.opts.Cancel = func() { cancelled = true }
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	mm := updated.(meetingModel)
	if !mm.quitting || !cancelled {
		t.Fatalf("quitting=%v cancelled=%v", mm.quitting, cancelled)
	}
}

func TestFormatMeetingDuration(t *testing.T) {
	if got := formatMeetingDuration(65 * time.Second); got != "01:05" {
		t.Fatalf("got %q", got)
	}
	if got := formatMeetingDuration(3661 * time.Second); got != "1:01:01" {
		t.Fatalf("got %q", got)
	}
}
