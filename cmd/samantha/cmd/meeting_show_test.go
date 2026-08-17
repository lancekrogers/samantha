package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func TestMeetingShowUnknownBundleFails(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		ref      string
		jsonOut  bool
		wantJSON bool
	}{
		{name: "unknown id, human", ref: "nope-20260816-101500.meeting"},
		{name: "unknown id, json", ref: "nope-20260816-101500.meeting", jsonOut: true, wantJSON: true},
		{name: "path outside any bundle", ref: filepath.Join(dir, "notes.md")},
		{name: "traversal", ref: "../../etc/passwd"},
		{name: "empty", ref: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, stdout, stderr := listTestCmd()
			err := runMeetingShow(cmd, dir, tt.ref, meetingShowOptions{JSON: tt.jsonOut})
			if err == nil {
				t.Fatalf("runMeetingShow(%q) = nil, want an error", tt.ref)
			}
			if !strings.Contains(err.Error(), "unknown bundle") {
				t.Errorf("error = %v, want it to name the unknown bundle", err)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing written for an unknown bundle", stdout)
			}
			if !tt.wantJSON {
				return
			}
			var problem map[string]string
			if err := json.Unmarshal([]byte(strings.SplitN(stderr.String(), "\n", 2)[0]), &problem); err != nil {
				t.Fatalf("stderr is not a JSON object: %v (%q)", err, stderr)
			}
			if problem["error"] != `meeting: unknown bundle "`+tt.ref+`"` {
				t.Fatalf("error = %q", problem["error"])
			}
		})
	}
}

func TestMeetingShowJSONCarriesTheEntryAndTheDocument(t *testing.T) {
	dir := t.TempDir()
	bundle := seedMeeting(t, dir, "weekly-sync-20260816-101500.meeting", "Weekly sync", 1, "docs")

	cmd, stdout, stderr := listTestCmd()
	if err := runMeetingShow(cmd, dir, filepath.Base(bundle), meetingShowOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingShow() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want machine mode to keep it clean", stderr)
	}
	var report meetingShowReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%s)", err, stdout)
	}
	if report.Meeting.Bundle != bundle || report.Meeting.Description != "Weekly sync" {
		t.Errorf("meeting = %+v, want the seeded bundle", report.Meeting)
	}
	if !strings.Contains(report.Document, "# Meeting: Weekly sync") {
		t.Errorf("document = %q, want the bundle's meeting.md", report.Document)
	}
	if !strings.Contains(report.Document, "action item") {
		t.Errorf("document = %q, want the note it recorded", report.Document)
	}
}

// TestMeetingShowResolvesEveryReferenceAUserHas covers the three things a
// person actually has after listing or tab-completing.
func TestMeetingShowResolvesEveryReferenceAUserHas(t *testing.T) {
	dir := t.TempDir()
	bundle := seedMeeting(t, dir, "standup-20260816-090000.meeting", "Standup", 0, "")

	refs := map[string]string{
		"bundle id":     filepath.Base(bundle),
		"bundle path":   bundle,
		"meeting.md":    filepath.Join(bundle, meetinglog.BundleDocumentName),
		"events.jsonl":  filepath.Join(bundle, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName),
		"trailing slug": bundle + string(filepath.Separator),
	}
	for name, ref := range refs {
		t.Run(name, func(t *testing.T) {
			cmd, stdout, _ := listTestCmd()
			if err := runMeetingShow(cmd, dir, ref, meetingShowOptions{JSON: true}); err != nil {
				t.Fatalf("runMeetingShow(%q) error = %v", ref, err)
			}
			var report meetingShowReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.Meeting.Bundle != bundle {
				t.Fatalf("bundle = %q, want %q", report.Meeting.Bundle, bundle)
			}
		})
	}
}

func TestMeetingShowDocumentWinsOverJSON(t *testing.T) {
	dir := t.TempDir()
	bundle := seedMeeting(t, dir, "standup-20260816-090000.meeting", "Standup", 1, "")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingShow(cmd, dir, filepath.Base(bundle), meetingShowOptions{JSON: true, Document: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), "# Meeting: Standup") {
		t.Fatalf("stdout = %q, want the raw document", stdout)
	}
	if strings.Contains(stdout.String(), `"meeting":`) {
		t.Error("--document emitted JSON as well as the document")
	}
}

func TestMeetingShowHumanOutputStatesTheEssentials(t *testing.T) {
	dir := t.TempDir()
	bundle := seedMeeting(t, dir, "weekly-sync-20260816-101500.meeting", "Weekly sync", 2, "docs")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingShow(cmd, dir, filepath.Base(bundle), meetingShowOptions{}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	for _, want := range []string{"Weekly sync", "2 notes", "→ docs (planned)", bundle} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to mention %q", out, want)
		}
	}
}

// TestMeetingShowWithoutNotesStillReports keeps a half-finished bundle
// readable: the counts and the route receipt are true even when the document
// is missing.
func TestMeetingShowWithoutNotesStillReports(t *testing.T) {
	dir := t.TempDir()
	bundle := seedMeeting(t, dir, "standup-20260816-090000.meeting", "Standup", 0, "")
	if err := os.Remove(filepath.Join(bundle, meetinglog.BundleDocumentName)); err != nil {
		t.Fatal(err)
	}

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingShow(cmd, dir, filepath.Base(bundle), meetingShowOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingShow() error = %v", err)
	}
	var report meetingShowReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Document != "" {
		t.Errorf("document = %q, want empty", report.Document)
	}
	if report.Meeting.Description != "Standup" {
		t.Errorf("meeting = %+v, want the entry regardless", report.Meeting)
	}
}
