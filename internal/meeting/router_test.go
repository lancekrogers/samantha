package meeting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func TestFileSinkRoutesAndKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	meetDir := filepath.Join(dir, "meetings")
	outDir := filepath.Join(dir, "export")
	if err := os.MkdirAll(meetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(meetDir, "standup.log")
	w, err := meetinglog.Create(path, "Standup", "fake")
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
	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}

	r := &Router{
		Cfg: Config{
			Mode: ModeAsk,
			Body: BodyNotes,
			Destinations: []Destination{
				{ID: "docs", Type: TypeFile, Path: outDir},
			},
		},
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	receipt, err := r.RouteByID(context.Background(), note, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s", receipt.Outcome)
	}
	if _, err := os.Stat(receipt.Detail); err != nil {
		t.Fatalf("export missing: %v", err)
	}
	// Original untouched.
	if _, err := os.Stat(summary.File); err != nil {
		t.Fatal(err)
	}
	// Provenance event appended.
	data, err := os.ReadFile(summary.JSONLFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"routed"`) {
		t.Fatalf("missing routed event:\n%s", data)
	}
}

func TestCampaignSinkShellsOut(t *testing.T) {
	var gotArgs []string
	r := &Router{
		Cfg: Config{
			Destinations: []Destination{
				{ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools", Capture: "intent"},
			},
		},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return "/bin/camp", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			gotArgs = append([]string{name}, args...)
			return []byte("created intent"), nil
		},
	}
	note := RenderedNote{
		Title: "Meeting: X (2026-07-20)",
		Body:  "# hi\n",
		Summary: meetinglog.Summary{
			Description: "X",
			StartedAt:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		},
	}
	receipt, err := r.RouteByID(context.Background(), note, "mytools")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s detail=%s", receipt.Outcome, receipt.Detail)
	}
	if len(gotArgs) < 6 || gotArgs[0] != "/bin/camp" || gotArgs[1] != "idea" {
		t.Fatalf("unexpected args: %v", gotArgs)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-c") || !strings.Contains(joined, "My_Tools") {
		t.Fatalf("missing campaign flag: %v", gotArgs)
	}
	if !strings.Contains(joined, "--body-file") {
		t.Fatalf("missing body-file: %v", gotArgs)
	}
}

func TestAvailableDestinationsHidesCampaignWithoutCamp(t *testing.T) {
	r := &Router{
		Cfg: Config{
			Destinations: []Destination{
				{ID: "c", Type: TypeCampaign, Campaign: "x"},
				{ID: "f", Type: TypeFile, Path: "/tmp"},
				{ID: "a", Type: TypeAppleNotes, Folder: "Meetings"},
			},
		},
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		GOOS:     "linux",
	}
	got := r.AvailableDestinations()
	if len(got) != 1 || got[0].ID != "f" {
		t.Fatalf("got %#v", got)
	}
}

func TestLoadSummaryAndResolveMostRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	w, err := meetinglog.Create(path, "A", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Ensure mtime ordering: second file later.
	time.Sleep(10 * time.Millisecond)
	path2 := filepath.Join(dir, "b.log")
	w2, err := meetinglog.Create(path2, "B", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	jsonl, err := ResolveMeetingFile(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(jsonl, "b.jsonl") {
		t.Fatalf("most recent = %s", jsonl)
	}
	s, err := LoadSummaryFromJSONL(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "B" {
		t.Fatalf("desc = %s", s.Description)
	}

	// Resolve from .log
	j2, err := ResolveMeetingFile(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(j2, "a.jsonl") {
		t.Fatalf("from log = %s", j2)
	}
}

func TestAppendRoutedEvent(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "m.jsonl")
	if err := os.WriteFile(jsonl, []byte("{\"type\":\"session_end\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AppendRoutedEvent(jsonl, Receipt{
		DestinationID: "docs",
		Type:          TypeFile,
		Outcome:       OutcomeRouted,
		Detail:        "/tmp/x.md",
		At:            time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(jsonl)
	var last map[string]any
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last["type"] != TypeRouted {
		t.Fatalf("last event = %#v", last)
	}
}

func TestBannerLine(t *testing.T) {
	s := BannerLine(Receipt{Outcome: OutcomeSkipped})
	if !strings.Contains(s, "local") {
		t.Fatal(s)
	}
	f := BannerLine(Receipt{Outcome: OutcomeFailed, Detail: "boom"})
	if !strings.Contains(f, "boom") {
		t.Fatal(f)
	}
}
