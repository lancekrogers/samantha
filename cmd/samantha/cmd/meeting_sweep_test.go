package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// sweepRouter files to a local directory, so a sweep test proves delivery
// without camp, osascript, or a network.
func sweepRouter(outDir string) *meeting.Router {
	return &meeting.Router{
		Cfg: meeting.Config{
			Mode: meeting.ModeAsk,
			Body: meeting.BodyNotes,
			Destinations: []meeting.Destination{
				{ID: "docs", Type: meeting.TypeFile, Path: outDir},
			},
		},
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
}

func decodeSweep(t *testing.T, raw string) meetingSweepReport {
	t.Helper()
	var report meetingSweepReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%s)", err, raw)
	}
	return report
}

// TestMeetingSweepFilesAPendingBundle is the whole point of the command: a
// meeting whose route never completed gets delivered, and the bundle stops
// being pending afterwards.
func TestMeetingSweepFilesAPendingBundle(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	pending := seedMeeting(t, dir, "pending-20260816-101500.meeting", "Pending", 1, "docs")
	seedMeeting(t, dir, "unplanned-20260816-101600.meeting", "Unplanned", 0, "")

	cmd, stdout, stderr := listTestCmd()
	if err := runMeetingSweep(cmd, sweepRouter(out), dir, meetingSweepOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingSweep() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want machine mode to keep it clean", stderr)
	}
	report := decodeSweep(t, stdout.String())
	if report.Attempted != 1 || report.Routed != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v, want 1 attempted / 1 routed", report)
	}
	result := report.Results[0]
	if result.Bundle != pending || result.ID != "pending-20260816-101500.meeting" {
		t.Errorf("result = %+v, want the pending bundle", result)
	}
	if result.DestinationID != "docs" || result.Outcome != meeting.OutcomeRouted || result.Error != "" {
		t.Errorf("result = %+v, want a clean route to docs", result)
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 1 {
		t.Fatalf("export dir = %v (err %v), want the filed note", entries, err)
	}

	// A second sweep has nothing to do: the routed event is durable.
	cmd, stdout, _ = listTestCmd()
	if err := runMeetingSweep(cmd, sweepRouter(out), dir, meetingSweepOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	if again := decodeSweep(t, stdout.String()); again.Attempted != 0 {
		t.Fatalf("second sweep = %+v, want nothing left pending", again)
	}
}

// TestMeetingSweepReportsFailuresWithoutFailing is the exit contract: a route
// that could not be delivered is data in the payload, not an error, because
// the bundle keeps its plan and the next sweep tries again.
func TestMeetingSweepReportsFailuresWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	seedMeeting(t, dir, "doomed-20260816-101500.meeting", "Doomed", 0, "nowhere")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingSweep(cmd, sweepRouter(t.TempDir()), dir, meetingSweepOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingSweep() error = %v, want a clean exit with the failure in the payload", err)
	}
	report := decodeSweep(t, stdout.String())
	if report.Attempted != 1 || report.Routed != 0 || report.Failed != 1 {
		t.Fatalf("report = %+v, want 1 attempted / 1 failed", report)
	}
	if report.Results[0].Error == "" {
		t.Error("a failed route reported no error text")
	}
	if !strings.Contains(report.Results[0].Error, "nowhere") {
		t.Errorf("error = %q, want it to name the destination", report.Results[0].Error)
	}
}

func TestMeetingSweepDryRunFilesNothing(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	seedMeeting(t, dir, "pending-20260816-101500.meeting", "Pending", 0, "docs")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingSweep(cmd, sweepRouter(out), dir, meetingSweepOptions{JSON: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	report := decodeSweep(t, stdout.String())
	if !report.DryRun || report.Attempted != 1 || report.Routed != 0 {
		t.Fatalf("report = %+v, want one pending meeting and nothing routed", report)
	}
	if report.Results[0].Outcome != outcomePending || report.Results[0].DestinationID != "docs" {
		t.Errorf("result = %+v, want it named as pending for docs", report.Results[0])
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 0 {
		t.Fatalf("export dir = %v (err %v), want a dry run to file nothing", entries, err)
	}
	// The bundle is untouched: no provenance was written.
	events, err := os.ReadFile(filepath.Join(dir, "pending-20260816-101500.meeting",
		meetinglog.BundleInternalDirName, meetinglog.BundleEventsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), meeting.TypeRouted) {
		t.Error("a dry run wrote route provenance")
	}
}

func TestMeetingSweepEmptyDirIsAnEmptyReport(t *testing.T) {
	cmd, stdout, _ := listTestCmd()
	missing := filepath.Join(t.TempDir(), "never-created")
	if err := runMeetingSweep(cmd, sweepRouter(t.TempDir()), missing, meetingSweepOptions{JSON: true}); err != nil {
		t.Fatalf("runMeetingSweep() error = %v, want nothing to sweep to be a clean exit", err)
	}
	report := decodeSweep(t, stdout.String())
	if report.Attempted != 0 || report.Results == nil {
		t.Fatalf("report = %s, want zeros and an empty array", stdout)
	}
	if !strings.Contains(stdout.String(), `"results":[]`) {
		t.Errorf("stdout = %s, want an empty array rather than null", stdout)
	}
}

func TestMeetingSweepHumanOutputSummarizes(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	seedMeeting(t, dir, "pending-20260816-101500.meeting", "Pending", 0, "docs")

	cmd, stdout, _ := listTestCmd()
	if err := runMeetingSweep(cmd, sweepRouter(out), dir, meetingSweepOptions{}); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, want := range []string{"pending-20260816-101500.meeting", "docs", "1 attempted, 1 routed, 0 failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("output = %q, want it to mention %q", text, want)
		}
	}
}
