package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// seedMeeting writes one closed bundle through the real recorder writer, so
// these tests read exactly what a recording leaves behind.
func seedMeeting(t *testing.T, dir, name, description string, notes int, routePlan string) string {
	t.Helper()
	bundle := filepath.Join(dir, name)
	w, err := meetinglog.CreateBundle(bundle, description, "fake", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if routePlan != "" {
		if err := w.WriteRoutePlan(routePlan, meeting.BodyFull); err != nil {
			t.Fatal(err)
		}
	}
	for range notes {
		if err := w.AddNote("action item"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func listTestCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(context.Background())
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, stdout, stderr
}

func TestMeetingListJSONIsAStableObject(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "weekly-sync-20260816-101500.meeting", "Weekly sync", 2, "camp:blockhead")

	cmd, stdout, stderr := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingList() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want machine mode to keep it clean", stderr)
	}

	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%s)", err, stdout)
	}
	if report.MeetingsDir != dir || report.Count != 1 || report.Truncated {
		t.Fatalf("report = %+v, want one meeting in %s", report, dir)
	}
	entry := report.Meetings[0]
	if entry.ID != "weekly-sync-20260816-101500.meeting" || entry.Description != "Weekly sync" {
		t.Errorf("entry = %+v, want the seeded bundle", entry)
	}
	if entry.Notes != 2 || entry.State != meeting.BundleStateReady || entry.Source != "mac" {
		t.Errorf("entry = %+v, want 2 notes, ready, source mac", entry)
	}
	if entry.Route == nil || entry.Route.Status != meeting.RouteStatusPlanned {
		t.Errorf("route = %+v, want planned", entry.Route)
	}
}

func TestMeetingListEmptyDirIsAnEmptyListNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created")
	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, missing, meetingListOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingList() error = %v, want nil for a missing dir", err)
	}
	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 0 || report.Meetings == nil {
		t.Fatalf("report = %s, want count 0 and an empty array", stdout)
	}
	if !strings.Contains(stdout.String(), `"meetings":[]`) {
		t.Errorf("stdout = %s, want an empty array rather than null", stdout)
	}
}

func TestMeetingListHumanRowSaysWhenWhatAndWhere(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "weekly-sync-20260816-101500.meeting", "Weekly sync", 1, "camp:blockhead")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{}); err != nil {
		t.Fatal(err)
	}
	row := strings.TrimSpace(stdout.String())
	for _, want := range []string{"Weekly sync", "→ camp:blockhead (planned)"} {
		if !strings.Contains(row, want) {
			t.Errorf("row = %q, want it to mention %q", row, want)
		}
	}
}

func TestMeetingListPendingKeepsOnlyRetryableRoutes(t *testing.T) {
	dir := t.TempDir()
	pending := seedMeeting(t, dir, "pending-20260816-101500.meeting", "Pending", 0, "docs")
	seedMeeting(t, dir, "unplanned-20260816-101600.meeting", "Unplanned", 0, "")
	delivered := seedMeeting(t, dir, "delivered-20260816-101700.meeting", "Delivered", 0, "docs")
	if err := meeting.AppendRoutedEvent(
		filepath.Join(delivered, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName),
		meeting.Receipt{DestinationID: "docs", Type: meeting.TypeFile, Outcome: meeting.OutcomeRouted,
			Detail: "/tmp/x.md", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true, Pending: true}); err != nil {
		t.Fatal(err)
	}
	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 1 {
		t.Fatalf("count = %d, want only the undelivered meeting: %+v", report.Count, report.Meetings)
	}
	if report.Meetings[0].Bundle != pending {
		t.Fatalf("bundle = %q, want %q", report.Meetings[0].Bundle, pending)
	}
}

func TestMeetingListLimitTruncates(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "a-20260816-101500.meeting", "A", 0, "")
	seedMeeting(t, dir, "b-20260816-101600.meeting", "B", 0, "")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true, Limit: 1}); err != nil {
		t.Fatal(err)
	}
	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 1 || !report.Truncated {
		t.Fatalf("report = %+v, want 1 entry and truncated", report)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		raw     string
		want    time.Time
		wantErr bool
	}{
		{name: "not a time at all", raw: "yesterday", wantErr: true},
		{name: "negative window", raw: "-3d", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
		{name: "rfc3339", raw: "2026-08-01T00:00:00Z", want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{name: "days", raw: "14d", want: now.Add(-14 * 24 * time.Hour)},
		{name: "hours", raw: "48h", want: now.Add(-48 * time.Hour)},
		{name: "minutes", raw: "90m", want: now.Add(-90 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSince(tt.raw, now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSince(%q) = %s, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSince(%q) error = %v", tt.raw, err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseSince(%q) = %s, want %s", tt.raw, got, tt.want)
			}
		})
	}
}

func TestMeetingListSinceDropsOlderMeetings(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "today-20260816-101500.meeting", "Today", 0, "")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true, Since: "1h"}); err != nil {
		t.Fatal(err)
	}
	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 1 {
		t.Fatalf("count = %d, want the just-written meeting", report.Count)
	}

	cmd, stdout, _ = listTestCmd()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true, Since: future}); err != nil {
		t.Fatal(err)
	}
	report = meetingListReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 0 {
		t.Fatalf("count = %d, want nothing since a future timestamp", report.Count)
	}
}

func TestMeetingListSkipsBundlesItCannotRead(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "good-20260816-101500.meeting", "Good", 0, "")
	torn := filepath.Join(dir, "torn-20260816-101600.meeting", meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(torn, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(torn, meetinglog.BundleEventsName), []byte("{oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingList(cmd, dir, meetingListOptions{JSON: true}); err != nil {
		t.Fatalf("one torn bundle failed the whole listing: %v", err)
	}
	var report meetingListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Count != 1 || report.Meetings[0].Description != "Good" {
		t.Fatalf("report = %+v, want the readable meeting", report)
	}
}
