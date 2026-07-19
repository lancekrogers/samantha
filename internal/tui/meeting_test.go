package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meetinglog"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

func sizedMeeting(t *testing.T, w, h int) meetingModel {
	t.Helper()
	ta := textarea.New()
	ta.SetHeight(meetingNoteHeight)
	ta.Focus()
	ta.KeyMap.InsertNewline.SetEnabled(false)
	m := meetingModel{
		opts: MeetingOpts{
			Description: "Standup",
			Path:        "/tmp/standup.log",
		},
		note:      ta,
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
	for _, want := range []string{"Meeting", "Standup", "listening", "Ctrl+B", "Enter"} {
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
	m, _ = m.handleListenMsg(meetingPartialMsg("hello world"))
	if m.partial != "hello world" {
		t.Fatalf("partial = %q", m.partial)
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
}

func TestMeetingNoteAndBookmarkPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.log")
	w, err := meetinglog.Create(path, "Notes test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	m := sizedMeeting(t, 80, 24)
	m.opts.Writer = w
	m.opts.Path = path

	m.note.SetValue("check budget")
	m, cmd := m.submitNote()
	if cmd != nil {
		t.Fatal("submitNote should not return error cmd")
	}
	if m.notes != 1 || m.note.Value() != "" {
		t.Fatalf("notes=%d draft=%q", m.notes, m.note.Value())
	}
	if !strings.Contains(m.View(), "check budget") {
		t.Fatal("note not visible in timeline")
	}

	m.note.SetValue("decision point")
	m, cmd = m.markImportant()
	if cmd != nil {
		t.Fatal("markImportant should not return error cmd")
	}
	if m.bookmarks != 1 {
		t.Fatalf("bookmarks=%d", m.bookmarks)
	}
	if !strings.Contains(m.View(), "IMPORTANT") {
		t.Fatal("bookmark not visible")
	}

	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Notes != 1 || sum.Bookmarks != 1 {
		t.Fatalf("summary notes=%d bookmarks=%d", sum.Notes, sum.Bookmarks)
	}
}

func TestMeetingStopKeys(t *testing.T) {
	cancelled := false
	m := sizedMeeting(t, 80, 24)
	m.opts.Cancel = func() { cancelled = true }
	// Plain 'q' types into the note field — does not stop.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	mm := updated.(meetingModel)
	if mm.quitting || cancelled {
		t.Fatal("plain q must type into notes, not stop")
	}
	// Ctrl+C stops.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	mm = updated.(meetingModel)
	if !mm.quitting || !cancelled {
		t.Fatalf("ctrl+c quitting=%v cancelled=%v", mm.quitting, cancelled)
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
