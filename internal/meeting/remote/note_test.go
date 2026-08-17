package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// TestNoteControlWritesADesktopNote proves a wire note is indistinguishable
// from one typed in the TUI: same event type, same document marker, same
// counter in the summary a route later renders from.
func TestNoteControlWritesADesktopNote(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)

	req := ControlRequest{
		Action:   "note",
		OffsetMs: 184300,
		Label:    "ignored",
		Text:     "decide the pricing tier next week",
	}
	if err := session.Control(context.Background(), req, clock.now); err != nil {
		t.Fatalf("Control(note) error = %v", err)
	}

	var notes []meetinglog.Event
	for _, event := range decodeEvents(t, session.BundlePath()) {
		if event.Type == meetinglog.TypeNote {
			notes = append(notes, event)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("note events = %d, want 1", len(notes))
	}
	if notes[0].Text != req.Text {
		t.Errorf("note text = %q, want %q", notes[0].Text, req.Text)
	}
	if notes[0].OffsetMs != req.OffsetMs {
		t.Errorf("note offset_ms = %d, want the client's %d", notes[0].OffsetMs, req.OffsetMs)
	}
	if notes[0].Label != "" {
		t.Errorf("note label = %q, want it dropped", notes[0].Label)
	}

	doc, err := os.ReadFile(filepath.Join(session.BundlePath(), meetinglog.BundleDocumentName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "📝 note: "+req.Text) {
		t.Errorf("meeting.md missing the note line:\n%s", doc)
	}

	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	summary := session.Status().Result
	if summary == nil {
		t.Fatal("no summary after stop")
	}
	if summary.Notes != 1 {
		t.Errorf("Summary.Notes = %d, want 1", summary.Notes)
	}
}

// TestNoteControlRejectsEmptyText covers the error cases first: an empty note
// is a counter bump with nothing behind it, and a note after stop is a note
// about a meeting that is over.
func TestNoteControlRejectsEmptyText(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "whitespace", text: "   \t\n "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, clock := janitorManager(t, newRecordingPipeline(nil))
			session := startSession(t, m)

			err := session.Control(context.Background(),
				ControlRequest{Action: "note", OffsetMs: 1000, Text: tt.text}, clock.now)
			if !errors.Is(err, ErrNoteText) {
				t.Fatalf("Control(note %q) error = %v, want ErrNoteText", tt.text, err)
			}
			for _, event := range decodeEvents(t, session.BundlePath()) {
				if event.Type == meetinglog.TypeNote {
					t.Fatal("a rejected note still reached the bundle")
				}
			}
			if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
				t.Fatal(err)
			}
			waitDone(t, session)
			if summary := session.Status().Result; summary != nil && summary.Notes != 0 {
				t.Errorf("Summary.Notes = %d, want 0", summary.Notes)
			}
		})
	}
}

func TestNoteAfterStopIsNotRecording(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)

	err := session.Control(context.Background(),
		ControlRequest{Action: "note", OffsetMs: 1000, Text: "too late"}, clock.now)
	if !errors.Is(err, ErrNotRecording) {
		t.Fatalf("Control(note) after stop = %v, want ErrNotRecording", err)
	}
}
