package meeting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// shellQuote wraps s for use inside a double-quoted sh -c fragment.
func shellQuote(s string) string {
	return strconv.Quote(s)
}

func TestFileSinkRoutesAndKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	meetDir := filepath.Join(dir, "meetings")
	outDir := filepath.Join(dir, "export")
	if err := os.MkdirAll(meetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(meetDir, "standup.meeting")
	w, err := meetinglog.CreateBundle(bundle, "Standup", "fake", "")
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

func TestCampaignSinkImportMeeting(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	// import-meeting requires meeting.md in the bundle.
	if err := os.WriteFile(filepath.Join(bundle, "meeting.md"), []byte("# Meeting\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotArgs []string
	sink := CampaignSink{
		Dest: Destination{ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools", Capture: CaptureMeeting},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return "/bin/camp", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// SupportsImportMeeting probes import-meeting --help first.
			if len(args) >= 4 && args[2] == "import-meeting" && args[3] == "--help" {
				return []byte("Import a meeting bundle into notes/meetings/"), nil
			}
			gotArgs = append([]string{name}, args...)
			return []byte(`{"schema_version":"intent-meeting-import/v1alpha1"}`), nil
		},
		// Empty dir → Runner path (argv capture without real camp).
		ResolveCampaignDir: func(context.Context, string) (string, error) { return "", nil },
	}
	note := RenderedNote{
		Title: "Meeting: X (2026-07-20)",
		Body:  "# hi\n\n## Summary\n\nnotes\n",
		Summary: meetinglog.Summary{
			Description: "X",
			Bundle:      bundle,
			StartedAt:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		},
	}
	receipt, err := sink.Route(context.Background(), note)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s detail=%s", receipt.Outcome, receipt.Detail)
	}
	if len(gotArgs) < 4 || gotArgs[0] != "/bin/camp" || gotArgs[1] != "idea" || gotArgs[2] != "notes" || gotArgs[3] != "import-meeting" {
		t.Fatalf("unexpected args: %v", gotArgs)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, bundle) {
		t.Fatalf("missing bundle path: %v", gotArgs)
	}
	if !strings.Contains(joined, "--summary-file") || !strings.Contains(joined, "--title") || !strings.Contains(joined, "--json") {
		t.Fatalf("missing import-meeting flags: %v", gotArgs)
	}
	if strings.Contains(joined, "idea add") || strings.Contains(joined, "--body-file") {
		t.Fatalf("should not use legacy idea add: %v", gotArgs)
	}
}

func TestCampaignSinkImportMeetingRequiresBundle(t *testing.T) {
	sink := CampaignSink{
		Dest:     Destination{ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools"},
		LookPath: func(string) (string, error) { return "/bin/camp", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("should not shell out without a bundle")
			return nil, nil
		},
	}
	_, err := sink.Route(context.Background(), RenderedNote{
		Title:   "Meeting: X",
		Body:    "# hi\n",
		Summary: meetinglog.Summary{Description: "X"},
	})
	if err == nil || !strings.Contains(err.Error(), "Summary.Bundle") {
		t.Fatalf("err = %v, want Summary.Bundle required", err)
	}
}

func TestCampaignSinkImportMeetingRunsInCampaignDir(t *testing.T) {
	// Production path: ResolveCampaignDir set + real exec with cmd.Dir.
	// Fake camp binary records PWD and argv.
	marker := filepath.Join(t.TempDir(), "camp-run.txt")
	campBin := filepath.Join(t.TempDir(), "camp")
	script := "#!/bin/sh\n" +
		"printf 'PWD=%s\\n' \"$(pwd)\" > " + shellQuote(marker) + "\n" +
		"printf 'ARGS=%s\\n' \"$*\" >> " + shellQuote(marker) + "\n" +
		"printf '%s\\n' '{\"schema_version\":\"intent-meeting-import/v1alpha1\"}'\n"
	if err := os.WriteFile(campBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	campaignDir := t.TempDir()
	// Real campaigns have a path; import-meeting only needs cwd for camp config
	// resolution — empty dir is enough to prove cmd.Dir.
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "meeting.md"), []byte("# Meeting\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := CampaignSink{
		Dest: Destination{ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools", Capture: CaptureMeeting},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return campBin, nil
			}
			return "", os.ErrNotExist
		},
		// Run is unused when dir is non-empty (runCommand path).
		Run: DefaultRunner,
		ResolveCampaignDir: func(context.Context, string) (string, error) {
			return campaignDir, nil
		},
	}
	note := RenderedNote{
		Title: "Meeting: Dir test",
		Body:  "## Summary\n\nok\n",
		Summary: meetinglog.Summary{
			Description: "Dir test",
			Bundle:      bundle,
			StartedAt:   time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		},
	}
	receipt, err := sink.Route(context.Background(), note)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s detail=%s", receipt.Outcome, receipt.Detail)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "PWD="+campaignDir) {
		t.Fatalf("camp not run in campaign dir:\n%s\nwant PWD=%s", text, campaignDir)
	}
	if !strings.Contains(text, "import-meeting") || !strings.Contains(text, bundle) {
		t.Fatalf("missing import-meeting argv:\n%s", text)
	}
}

func TestResolveCampaignDirFromList(t *testing.T) {
	wantPath := "/abs/path/to/My_Tools"
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		// ListCampaigns uses LookPath result as the executable name.
		if !strings.HasSuffix(name, "camp") || len(args) < 2 || args[0] != "list" {
			t.Fatalf("unexpected call %s %v", name, args)
		}
		return []byte(`[{"id":"abc","name":"My_Tools","path":"` + wantPath + `","status":"active"}]`), nil
	}
	got, err := resolveCampaignDir(context.Background(), run, func(string) (string, error) {
		return "/bin/camp", nil
	}, "My_Tools")
	if err != nil {
		t.Fatal(err)
	}
	if got != wantPath {
		t.Fatalf("got %q want %q", got, wantPath)
	}
}

func TestNormalizeCampaignCapture(t *testing.T) {
	cases := map[string]string{
		"":               CaptureMeeting,
		"meeting":        CaptureMeeting,
		"import-meeting": CaptureMeeting,
		"intent":         CaptureIntent,
		"note":           CaptureNote,
		"weird":          CaptureMeeting,
	}
	for in, want := range cases {
		if got := NormalizeCampaignCapture(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveCampaignDirExactIDOnly(t *testing.T) {
	// Partial ID must not match a longer campaign id.
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`[
			{"id":"abcdef12-3456","name":"Other","path":"/other"},
			{"id":"abc","name":"Exact","path":"/exact"}
		]`), nil
	}
	look := func(string) (string, error) { return "camp", nil }
	got, err := resolveCampaignDir(context.Background(), run, look, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/exact" {
		t.Fatalf("got %q, want /exact (exact id, not prefix of abcdef12)", got)
	}
	// Name match still works.
	got, err = resolveCampaignDir(context.Background(), run, look, "Other")
	if err != nil || got != "/other" {
		t.Fatalf("name match: got %q err %v", got, err)
	}
}

func TestCampaignSinkImportMeetingEmbedsTagsInSummary(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "meeting.md"), []byte("# M\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var summaryBody string
	sink := CampaignSink{
		Dest: Destination{
			ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools",
			Capture: CaptureMeeting, Tags: []string{"voice", "meeting"},
		},
		LookPath: func(string) (string, error) { return "/bin/camp", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 4 && args[2] == "import-meeting" && args[3] == "--help" {
				return []byte("Import a meeting bundle into notes/meetings/"), nil
			}
			for i, a := range args {
				if a == "--summary-file" && i+1 < len(args) {
					// Read while the temp file still exists (Route defers Remove).
					data, err := os.ReadFile(args[i+1])
					if err != nil {
						return nil, err
					}
					summaryBody = string(data)
				}
			}
			return []byte(`{"schema_version":"intent-meeting-import/v1alpha1"}`), nil
		},
		ResolveCampaignDir: func(context.Context, string) (string, error) { return "", nil },
	}
	_, err := sink.Route(context.Background(), RenderedNote{
		Title: "T",
		Body:  "## Summary\n\nhello\n",
		Summary: meetinglog.Summary{
			Bundle: bundle, Description: "T",
			StartedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summaryBody, "**Route tags:** voice, meeting") {
		t.Fatalf("summary missing tags:\n%s", summaryBody)
	}
}

func TestCampaignSinkLegacyIntentAddWithoutImporter(t *testing.T) {
	// Old camp without import-meeting: capture:intent still uses idea add.
	var gotArgs []string
	sink := CampaignSink{
		Dest: Destination{ID: "mytools", Type: TypeCampaign, Campaign: "My_Tools", Capture: CaptureIntent},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return "/bin/camp", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// Probe fails → SupportsImportMeeting returns error.
			if len(args) >= 3 && args[0] == "idea" && args[1] == "notes" && args[2] == "import-meeting" {
				return []byte("Error: unknown command"), fmt.Errorf("exit 1")
			}
			gotArgs = append([]string{name}, args...)
			return []byte("created intent"), nil
		},
	}
	note := RenderedNote{
		Title: "Meeting: X (2026-07-20)",
		Body:  "# hi\n",
		Summary: meetinglog.Summary{
			Description: "X",
			// Bundle present but importer unsupported → idea add fallback.
			Bundle:    filepath.Join(t.TempDir(), "x.meeting"),
			StartedAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		},
	}
	_ = os.MkdirAll(note.Summary.Bundle, 0o700)
	_ = os.WriteFile(filepath.Join(note.Summary.Bundle, "meeting.md"), []byte("# m\n"), 0o600)
	receipt, err := sink.Route(context.Background(), note)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s detail=%s", receipt.Outcome, receipt.Detail)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "idea") || !strings.Contains(joined, "add") {
		t.Fatalf("expected idea add: %v", gotArgs)
	}
	if !strings.Contains(joined, "-c") || !strings.Contains(joined, "My_Tools") {
		t.Fatalf("missing campaign flag: %v", gotArgs)
	}
	if !strings.Contains(joined, "--body-file") {
		t.Fatalf("missing body-file: %v", gotArgs)
	}
}

func TestCampaignSinkIntentUpgradesToImportMeeting(t *testing.T) {
	// capture:intent with a modern camp + bundle must use notes/meetings, not inbox.
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "meeting.md"), []byte("# Meeting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	sink := CampaignSink{
		Dest: Destination{ID: "jobsearch", Type: TypeCampaign, Campaign: "JobSearch", Capture: CaptureIntent},
		LookPath: func(name string) (string, error) {
			if name == "camp" {
				return "/bin/camp", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// SupportsImportMeeting probes --help.
			if len(args) >= 4 && args[2] == "import-meeting" && args[3] == "--help" {
				return []byte("Import a meeting bundle into notes/meetings/"), nil
			}
			gotArgs = append([]string{name}, args...)
			return []byte(`{"schema_version":"intent-meeting-import/v1alpha1"}`), nil
		},
		ResolveCampaignDir: func(context.Context, string) (string, error) { return "", nil },
	}
	_, err := sink.Route(context.Background(), RenderedNote{
		Title: "Meeting: X",
		Body:  "# hi\n",
		Summary: meetinglog.Summary{
			Description: "X",
			Bundle:      bundle,
			StartedAt:   time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "import-meeting") {
		t.Fatalf("expected import-meeting for capture:intent, got %v", gotArgs)
	}
	if strings.Contains(joined, "idea add") || strings.Contains(joined, "--body-file") {
		t.Fatalf("must not fall back to idea add: %v", gotArgs)
	}
}

func TestCampaignSinkMeetingFailsClosedWithoutImporter(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "x.meeting")
	_ = os.MkdirAll(bundle, 0o700)
	_ = os.WriteFile(filepath.Join(bundle, "meeting.md"), []byte("# m\n"), 0o600)
	sink := CampaignSink{
		Dest:     Destination{ID: "c", Type: TypeCampaign, Campaign: "JobSearch", Capture: CaptureMeeting},
		LookPath: func(string) (string, error) { return "/bin/camp", nil },
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("Error: unknown command"), fmt.Errorf("exit 1")
		},
	}
	_, err := sink.Route(context.Background(), RenderedNote{
		Title:   "T",
		Body:    "# hi\n",
		Summary: meetinglog.Summary{Bundle: bundle, Description: "T"},
	})
	if err == nil || (!errors.Is(err, ErrImportMeetingUnsupported) && !strings.Contains(err.Error(), "import-meeting")) {
		t.Fatalf("err = %v, want import-meeting unsupported", err)
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
	bundleA := filepath.Join(dir, "a.meeting")
	w, err := meetinglog.CreateBundle(bundleA, "A", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Ensure the second session starts later.
	time.Sleep(10 * time.Millisecond)
	bundleB := filepath.Join(dir, "b.meeting")
	w2, err := meetinglog.CreateBundle(bundleB, "B", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	// Routing mutates the old event file but must not make it the latest
	// recording; discovery is ordered by session_start, not mtime.
	if err := AppendRoutedEvent(w.JSONLPath(), Receipt{Outcome: OutcomeSkipped, At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	jsonl, err := ResolveMeetingFile(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if jsonl != w2.JSONLPath() {
		t.Fatalf("most recent = %s, want %s", jsonl, w2.JSONLPath())
	}
	s, err := LoadSummaryFromJSONL(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "B" {
		t.Fatalf("desc = %s", s.Description)
	}

	// Resolve from the bundle and its canonical document.
	j2, err := ResolveMeetingFile(dir, bundleA)
	if err != nil {
		t.Fatal(err)
	}
	if j2 != w.JSONLPath() {
		t.Fatalf("from bundle = %s", j2)
	}
	j2, err = ResolveMeetingFile(dir, w.Path())
	if err != nil {
		t.Fatal(err)
	}
	if j2 != w.JSONLPath() {
		t.Fatalf("from meeting.md = %s", j2)
	}
}

func TestResolveMeetingBundleRejectsFlatArtifacts(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "new-20260722-090000.meeting")
	bundled, err := meetinglog.CreateBundle(bundlePath, "Bundled", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := bundled.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
		Status:   "complete",
		Segments: []meetinglog.SpeakerSegment{{Label: "speaker-1"}, {Label: "speaker-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := bundled.Close(); err != nil {
		t.Fatal(err)
	}

	wantEvents := filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName)
	for _, input := range []string{bundlePath, filepath.Join(bundlePath, meetinglog.BundleDocumentName)} {
		got, err := ResolveMeetingFile(dir, input)
		if err != nil {
			t.Fatal(err)
		}
		if got != wantEvents {
			t.Fatalf("ResolveMeetingFile(%q) = %q, want %q", input, got, wantEvents)
		}
	}
	latest, err := ResolveMeetingFile(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest != wantEvents {
		t.Fatalf("latest = %q, want bundled %q", latest, wantEvents)
	}
	summary, err := LoadSummaryFromJSONL(latest)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Bundle != bundlePath || summary.File != filepath.Join(bundlePath, meetinglog.BundleDocumentName) || summary.SpeakerCount != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, name := range []string{"old.log", "old.jsonl"} {
		flat := filepath.Join(dir, name)
		if err := os.WriteFile(flat, []byte("test artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveMeetingFile(dir, flat); err == nil {
			t.Fatalf("flat artifact %s must be rejected", flat)
		}
	}
	if _, err := LoadSummaryFromJSONL(filepath.Join(dir, "old.jsonl")); err == nil {
		t.Fatal("flat event stream must be rejected")
	}
}

func TestAppendRoutedEvent(t *testing.T) {
	w, err := meetinglog.CreateBundle(filepath.Join(t.TempDir(), "m.meeting"), "M", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	err = AppendRoutedEvent(summary.JSONLFile, Receipt{
		DestinationID: "docs",
		Type:          TypeFile,
		Outcome:       OutcomeRouted,
		Detail:        "/tmp/x.md",
		At:            time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(summary.JSONLFile)
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
