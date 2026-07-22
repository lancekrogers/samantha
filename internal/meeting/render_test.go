package meeting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func TestRenderEventsNotesScope(t *testing.T) {
	start := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	summary := meetinglog.Summary{
		Description: "Standup",
		Bundle:      "/tmp/standup.meeting",
		File:        "/tmp/standup.meeting/meeting.md",
		JSONLFile:   "/tmp/standup.meeting/.samantha/events.jsonl",
		StartedAt:   start,
		EndedAt:     start.Add(5 * time.Minute),
		Utterances:  2,
		Notes:       1,
		Bookmarks:   1,
	}
	events := []meetinglog.Event{
		{Type: meetinglog.TypeSessionStart, Desc: "Standup", TS: start.Format(time.RFC3339)},
		{Type: meetinglog.TypeUtterance, Text: "hello world", OffsetMs: 1000},
		{Type: meetinglog.TypeNote, Text: "ship the route feature", OffsetMs: 2000},
		{Type: meetinglog.TypeBookmark, Label: "important", Text: "decision", OffsetMs: 3000},
		{Type: meetinglog.TypeUtterance, Text: "bye", OffsetMs: 4000},
		{Type: meetinglog.TypeSessionEnd, TS: start.Add(5 * time.Minute).Format(time.RFC3339)},
	}

	note := RenderEvents(summary, events, BodyNotes)
	if !strings.Contains(note.Title, "Standup") {
		t.Fatalf("title = %q", note.Title)
	}
	if !strings.Contains(note.Body, "ship the route feature") {
		t.Fatalf("missing note body:\n%s", note.Body)
	}
	if !strings.Contains(note.Body, "★") {
		t.Fatalf("missing bookmark:\n%s", note.Body)
	}
	// Notes scope must embed the transcript (campaign intents cannot rely on local paths).
	if !strings.Contains(note.Body, "hello world") || !strings.Contains(note.Body, "bye") {
		t.Fatalf("notes scope missing transcript utterances:\n%s", note.Body)
	}
	if strings.Contains(note.Body, "Full transcript kept locally") {
		t.Fatalf("notes scope should not point at local path instead of embedding:\n%s", note.Body)
	}

	full := RenderEvents(summary, events, BodyFull)
	if !strings.Contains(full.Body, "hello world") || !strings.Contains(full.Body, "bye") {
		t.Fatalf("full scope missing utterances:\n%s", full.Body)
	}
}

func TestRenderEventsPrefersSpeakerAttributedTranscript(t *testing.T) {
	summary := meetinglog.Summary{Description: "sync", Utterances: 1, SpeakerStatus: "complete", SpeakerCount: 2}
	events := []meetinglog.Event{
		{Type: meetinglog.TypeUtterance, Text: "original", OffsetMs: 1200},
		{Type: meetinglog.TypeSpeakerUtterance, Text: "original", Label: "speaker-2", OffsetMs: 1200},
	}
	note := RenderEvents(summary, events, BodyFull)
	if !strings.Contains(note.Body, "**speaker-2:** original") {
		t.Fatalf("attributed transcript missing:\n%s", note.Body)
	}
	if !strings.Contains(note.Body, "**Speaker analysis:** complete") || !strings.Contains(note.Body, "**Detected speakers:** 2") {
		t.Fatalf("speaker summary missing:\n%s", note.Body)
	}
	if strings.Count(note.Body, "original") != 1 {
		t.Fatalf("raw + attributed transcript duplicated:\n%s", note.Body)
	}
}

func TestRenderFromMeetingBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "m.meeting")
	w, err := meetinglog.CreateBundle(bundle, "Design review", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("follow up on routing"); err != nil {
		t.Fatal(err)
	}
	if err := w.AddBookmark("important", "ship v1"); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note.Body, "follow up on routing") {
		t.Fatalf("render missing note:\n%s", note.Body)
	}
	// Original bundle remains present.
	if _, err := os.Stat(summary.Bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(summary.File); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(summary.JSONLFile); err != nil {
		t.Fatal(err)
	}
}

func TestIntentTitle(t *testing.T) {
	s := meetinglog.Summary{
		Description: "Weekly planning",
		StartedAt:   time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC),
	}
	got := IntentTitle(s)
	want := "Meeting: Weekly planning (2026-07-20)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
