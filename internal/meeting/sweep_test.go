package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

func sweepTestRouter(outDir string) *Router {
	return &Router{
		Cfg: Config{
			Mode: ModeAsk,
			Body: BodyNotes,
			Destinations: []Destination{
				{ID: "docs", Type: TypeFile, Path: outDir},
			},
		},
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
}

// planBundle fabricates a recorded bundle carrying a durable route plan.
func planBundle(t *testing.T, meetingsDir, name, destID string, finished bool) string {
	t.Helper()
	bundle := filepath.Join(meetingsDir, name)
	w, err := meetinglog.CreateBundle(bundle, "Standup", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("action item"); err != nil {
		t.Fatal(err)
	}
	if destID != "" {
		if err := w.WriteRoutePlan(destID, "dest"); err != nil {
			t.Fatal(err)
		}
	}
	if finished {
		if _, err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Cleanup(func() { _, _ = w.Close() })
	}
	return bundle
}

func bundleEvents(t *testing.T, bundle string) string {
	t.Helper()
	data, err := os.ReadFile(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSweepDeliversPendingRoute(t *testing.T) {
	dir := t.TempDir()
	meetingsDir := filepath.Join(dir, "meetings")
	outDir := filepath.Join(dir, "export")
	if err := os.MkdirAll(meetingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := planBundle(t, meetingsDir, "standup.meeting", "docs", true)

	results := SweepPendingRoutes(context.Background(), sweepTestRouter(outDir), meetingsDir)
	if len(results) != 1 {
		t.Fatalf("results = %+v, want 1", results)
	}
	if results[0].Err != nil {
		t.Fatalf("sweep route failed: %v", results[0].Err)
	}
	if results[0].Receipt.Outcome != OutcomeRouted {
		t.Fatalf("outcome = %s", results[0].Receipt.Outcome)
	}
	if _, err := os.Stat(results[0].Receipt.Detail); err != nil {
		t.Fatalf("exported note missing: %v", err)
	}
	if !strings.Contains(bundleEvents(t, bundle), `"type":"routed"`) {
		t.Fatal("missing routed provenance after sweep")
	}

	// Delivered: a second sweep must not re-route.
	if again := SweepPendingRoutes(context.Background(), sweepTestRouter(outDir), meetingsDir); len(again) != 0 {
		t.Fatalf("second sweep re-routed: %+v", again)
	}
}

func TestSweepSkipsUnfinishedAndUnplannedBundles(t *testing.T) {
	dir := t.TempDir()
	meetingsDir := filepath.Join(dir, "meetings")
	if err := os.MkdirAll(meetingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Still recording (route plan, no session_end) and finished-without-plan.
	planBundle(t, meetingsDir, "live.meeting", "docs", false)
	planBundle(t, meetingsDir, "local.meeting", "", true)

	if results := SweepPendingRoutes(context.Background(), sweepTestRouter(t.TempDir()), meetingsDir); len(results) != 0 {
		t.Fatalf("sweep must skip unfinished/unplanned bundles: %+v", results)
	}
}

func TestSweepFailureIsDurableAndCapped(t *testing.T) {
	dir := t.TempDir()
	meetingsDir := filepath.Join(dir, "meetings")
	if err := os.MkdirAll(meetingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := planBundle(t, meetingsDir, "ghost.meeting", "ghost", true)

	for attempt := 1; attempt <= SweepMaxAttempts; attempt++ {
		results := SweepPendingRoutes(context.Background(), sweepTestRouter(t.TempDir()), meetingsDir)
		if len(results) != 1 || results[0].Err == nil {
			t.Fatalf("attempt %d: results = %+v, want one failure", attempt, results)
		}
	}
	if got := strings.Count(bundleEvents(t, bundle), `"type":"route_failed"`); got != SweepMaxAttempts {
		t.Fatalf("route_failed events = %d, want %d", got, SweepMaxAttempts)
	}
	// Attempt cap reached: no further automatic retries.
	if results := SweepPendingRoutes(context.Background(), sweepTestRouter(t.TempDir()), meetingsDir); len(results) != 0 {
		t.Fatalf("sweep exceeded retry cap: %+v", results)
	}
}

// A bundle whose event stream cannot be parsed must not enter the retry loop
// at all — corrupt bundles are a CLI-recovery case, never an every-launch
// failure banner.
func TestSweepSkipsUnreadableBundle(t *testing.T) {
	dir := t.TempDir()
	meetingsDir := filepath.Join(dir, "meetings")
	internal := filepath.Join(meetingsDir, "bad.meeting", meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internal, meetinglog.BundleEventsName), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if results := SweepPendingRoutes(context.Background(), sweepTestRouter(t.TempDir()), meetingsDir); len(results) != 0 {
			t.Fatalf("sweep %d picked up unreadable bundle: %+v", i, results)
		}
	}
}

func TestRouteFailureAppendsRouteFailedProvenance(t *testing.T) {
	dir := t.TempDir()
	meetingsDir := filepath.Join(dir, "meetings")
	if err := os.MkdirAll(meetingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := planBundle(t, meetingsDir, "x.meeting", "", true)
	summary, err := LoadSummaryFromJSONL(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sweepTestRouter(t.TempDir()).RouteByID(context.Background(), note, "nope"); err == nil {
		t.Fatal("expected unknown destination error")
	}
	if !strings.Contains(bundleEvents(t, bundle), `"type":"route_failed"`) {
		t.Fatal("failed route must append route_failed provenance")
	}
}
