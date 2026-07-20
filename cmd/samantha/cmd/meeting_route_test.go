package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// finishedMeeting builds a closed meeting pair under dir for routing tests.
func finishedMeeting(t *testing.T, dir, desc string) meetinglog.Summary {
	t.Helper()
	path := filepath.Join(dir, "session.log")
	w, err := meetinglog.Create(path, desc, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("action item"); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func routeTestCmd(stdout, stderr *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

func TestMaybeRouteAfterRecordJSONExplicitRouteKeepsStdoutClean(t *testing.T) {
	dir := t.TempDir()
	export := filepath.Join(dir, "export")
	summary := finishedMeeting(t, dir, "JSON route")

	cfg := &config.Config{
		Meeting: config.MeetingConfig{
			Route: config.MeetingRouteConfig{
				Mode: meeting.ModeAsk, // ignored when --route is set
				Body: meeting.BodyNotes,
				Destinations: []config.MeetingDestinationConfig{
					{ID: "docs", Type: meeting.TypeFile, Path: export},
				},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	cmd := routeTestCmd(&stdout, &stderr)
	err := maybeRouteAfterRecord(cmd, cfg, summary, meetingOptions{
		JSON:    true,
		RouteTo: "docs",
	})
	if err != nil {
		t.Fatalf("maybeRouteAfterRecord: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("JSON mode stdout must stay empty after routing, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Meeting notes routed") {
		t.Fatalf("expected routing banner on stderr, got %q", stderr.String())
	}
	// Export actually landed.
	entries, err := filepath.Glob(filepath.Join(export, "*.md"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("export files = %v err=%v", entries, err)
	}
}

func TestMaybeRouteAfterRecordJSONAutoRouteKeepsStdoutClean(t *testing.T) {
	dir := t.TempDir()
	export := filepath.Join(dir, "export")
	summary := finishedMeeting(t, dir, "JSON auto")

	cfg := &config.Config{
		Meeting: config.MeetingConfig{
			Route: config.MeetingRouteConfig{
				Mode:    meeting.ModeAuto,
				Default: "docs",
				Body:    meeting.BodyNotes,
				Destinations: []config.MeetingDestinationConfig{
					{ID: "docs", Type: meeting.TypeFile, Path: export},
				},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	cmd := routeTestCmd(&stdout, &stderr)
	err := maybeRouteAfterRecord(cmd, cfg, summary, meetingOptions{
		JSON: true,
		// no RouteTo — auto uses default
	})
	if err != nil {
		t.Fatalf("maybeRouteAfterRecord: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("JSON mode stdout must stay empty after auto-route, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Meeting notes routed") {
		t.Fatalf("expected routing banner on stderr, got %q", stderr.String())
	}
}

func TestMaybeRouteAfterRecordHumanModeBannerOnStdout(t *testing.T) {
	dir := t.TempDir()
	export := filepath.Join(dir, "export")
	summary := finishedMeeting(t, dir, "Human route")

	cfg := &config.Config{
		Meeting: config.MeetingConfig{
			Route: config.MeetingRouteConfig{
				Body: meeting.BodyNotes,
				Destinations: []config.MeetingDestinationConfig{
					{ID: "docs", Type: meeting.TypeFile, Path: export},
				},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	cmd := routeTestCmd(&stdout, &stderr)
	err := maybeRouteAfterRecord(cmd, cfg, summary, meetingOptions{
		JSON:    false,
		RouteTo: "docs",
	})
	if err != nil {
		t.Fatalf("maybeRouteAfterRecord: %v", err)
	}
	if !strings.Contains(stdout.String(), "Meeting notes routed") {
		t.Fatalf("human mode banner should be on stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("human mode should not write status to stderr, got %q", stderr.String())
	}
}

func TestRouteAndPrintJSONDoesNotCorruptJSONStream(t *testing.T) {
	// Simulates the record --json flow: a JSON summary on stdout, then routing.
	dir := t.TempDir()
	export := filepath.Join(dir, "export")
	summary := finishedMeeting(t, dir, "Stream")

	cfg := meeting.Config{
		Body: meeting.BodyNotes,
		Destinations: []meeting.Destination{
			{ID: "docs", Type: meeting.TypeFile, Path: export},
		},
	}
	router := meeting.NewDefaultRouter(cfg)

	var stdout, stderr bytes.Buffer
	cmd := routeTestCmd(&stdout, &stderr)

	// Emit the final summary the same way runMeetingRecord does in JSON mode.
	if err := json.NewEncoder(&stdout).Encode(summary); err != nil {
		t.Fatal(err)
	}
	if err := routeAndPrint(cmd, router, summary, meeting.BodyNotes, "docs", true); err != nil {
		t.Fatal(err)
	}

	// stdout must remain a single JSON object (optional trailing newline).
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var got meetinglog.Summary
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not valid JSON summary: %v\nstdout=%q", err, stdout.String())
	}
	if dec.More() {
		t.Fatalf("stdout has extra tokens after JSON summary: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Meeting notes routed") {
		t.Fatalf("banner missing on stderr: %q", stderr.String())
	}
}
