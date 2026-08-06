package ideas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/netapi"
)

// fixture is a real .meeting bundle with an open writer — everything goes
// through the production APIs, so the on-disk format Resolve reads is honest.
// Utterance offsets are wall-clock-derived with estimated starts, so tests
// use windows wide enough that estimator slop cannot flip an overlap.
type fixture struct {
	t          *testing.T
	bundlePath string
	writer     *meetinglog.Writer
	base       time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := time.Now()
	bundlePath := filepath.Join(t.TempDir(), "standup.meeting")
	writer, err := meetinglog.CreateBundle(bundlePath, "Standup", "test-stt")
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	t.Cleanup(func() { _, _ = writer.Close() })
	return &fixture{t: t, bundlePath: bundlePath, writer: writer, base: base}
}

func (f *fixture) utterance(endOffsetMs int64, text string) {
	f.t.Helper()
	err := f.writer.OnUtterance(listen.Utterance{
		Text: text,
		At:   f.base.Add(time.Duration(endOffsetMs) * time.Millisecond),
	})
	if err != nil {
		f.t.Fatalf("OnUtterance() error = %v", err)
	}
}

func (f *fixture) control(kind string, offsetMs int64, label, text string) {
	f.t.Helper()
	if err := f.writer.AppendControl(kind, offsetMs, label, text); err != nil {
		f.t.Fatalf("AppendControl(%s) error = %v", kind, err)
	}
}

func (f *fixture) resolve(file FileFunc) Report {
	f.t.Helper()
	report, err := Resolve(context.Background(), f.bundlePath, f.writer, file)
	if err != nil {
		f.t.Fatalf("Resolve() error = %v", err)
	}
	return report
}

func TestResolveFilesSpansFromTranscript(t *testing.T) {
	f := newFixture(t)
	f.utterance(8000, "cache the receipts")
	f.control(meetinglog.TypeIdeaStart, 5000, "span-a", "")
	f.utterance(15000, "file a bug about pairing")
	f.control(meetinglog.TypeIdeaEnd, 20000, "span-a", "")
	f.utterance(60000, "tail talk")

	var filed []Resolved
	report := f.resolve(func(_ context.Context, idea Resolved) error {
		filed = append(filed, idea)
		return nil
	})
	if report.Filed != 1 || report.Unresolved != 0 {
		t.Fatalf("report = %+v, want 1 filed", report)
	}
	if len(filed) != 1 || filed[0].SpanID != "span-a" {
		t.Fatalf("filed = %+v", filed)
	}
	if !strings.Contains(filed[0].Body, "cache the receipts") ||
		!strings.Contains(filed[0].Body, "file a bug about pairing") ||
		strings.Contains(filed[0].Body, "tail talk") {
		t.Fatalf("body = %q", filed[0].Body)
	}
}

// The dedupe contract: a re-run files only spans without an idea_filed marker.
func TestResolveIsRerunSafe(t *testing.T) {
	f := newFixture(t)
	f.utterance(5000, "first idea words")
	f.control(meetinglog.TypeIdeaStart, 1000, "one", "")
	f.control(meetinglog.TypeIdeaEnd, 8000, "one", "")
	f.utterance(25000, "second idea words")
	f.control(meetinglog.TypeIdeaStart, 20000, "two", "")
	f.control(meetinglog.TypeIdeaEnd, 30000, "two", "")

	calls := map[string]int{}
	f.resolve(func(_ context.Context, idea Resolved) error {
		calls[idea.SpanID]++
		if idea.SpanID == "two" {
			return errors.New("sink offline")
		}
		return nil
	})

	report := f.resolve(func(_ context.Context, idea Resolved) error {
		calls[idea.SpanID]++
		return nil
	})
	if calls["one"] != 1 || calls["two"] != 2 {
		t.Fatalf("filing calls = %v, want one=1 two=2", calls)
	}
	if report.AlreadyFiled != 1 || report.Filed != 1 {
		t.Fatalf("re-run report = %+v", report)
	}
}

func TestResolveSurfacesSilentSpansAsNotes(t *testing.T) {
	f := newFixture(t)
	f.utterance(5000, "early words far from the span")
	f.control(meetinglog.TypeIdeaStart, 100000, "quiet", "")
	f.control(meetinglog.TypeIdeaEnd, 110000, "quiet", "")

	report := f.resolve(func(_ context.Context, _ Resolved) error {
		t.Fatal("a silent span must not be filed")
		return nil
	})
	if report.Unresolved != 1 {
		t.Fatalf("report = %+v, want 1 unresolved", report)
	}
	raw, err := os.ReadFile(filepath.Join(f.bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "idea_unresolved") {
		t.Fatal("silent span left no trace in the bundle")
	}
}

// A start the user never closed still resolves — to the end of the meeting.
func TestResolveUnclosedSpanRunsToMeetingEnd(t *testing.T) {
	f := newFixture(t)
	f.control(meetinglog.TypeIdeaStart, 10000, "open", "")
	f.utterance(20000, "the forgotten thought")

	var filed []Resolved
	report := f.resolve(func(_ context.Context, idea Resolved) error {
		filed = append(filed, idea)
		return nil
	})
	if report.Filed != 1 || len(filed) != 1 {
		t.Fatalf("report = %+v filed = %+v", report, filed)
	}
	if !strings.Contains(filed[0].Body, "forgotten thought") {
		t.Fatalf("body = %q", filed[0].Body)
	}
}

// Typed text attached to idea_end leads the body even with no speech.
func TestResolveUsesIdeaEndTextAsBody(t *testing.T) {
	f := newFixture(t)
	f.control(meetinglog.TypeIdeaStart, 1000, "typed", "")
	f.control(meetinglog.TypeIdeaEnd, 2000, "typed", "check the AEC latency")

	var filed []Resolved
	report := f.resolve(func(_ context.Context, idea Resolved) error {
		filed = append(filed, idea)
		return nil
	})
	if report.Filed != 1 || len(filed) != 1 || filed[0].Body != "check the AEC latency" {
		t.Fatalf("report = %+v filed = %+v", report, filed)
	}
}

// The crash-consistency contract: filing succeeded but the idea_filed marker
// never landed (crash, closed writer, disk error). The durable receipt is the
// sink's deterministic create-if-absent key, so the re-run re-resolves the
// span, re-files through the sink — and the sink refuses to duplicate.
func TestResolveRerunAfterMarkerFailureDoesNotDuplicate(t *testing.T) {
	f := newFixture(t)
	f.utterance(5000, "the idea words")
	f.control(meetinglog.TypeIdeaStart, 1000, "span-x", "")
	f.control(meetinglog.TypeIdeaEnd, 8000, "span-x", "")

	// Marker persistence fails from here on: the writer is closed, exactly
	// as if the process died right after the sink write.
	if _, err := f.writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	sinkDir := filepath.Join(t.TempDir(), "intents")
	sink := func(_ context.Context, idea Resolved) error {
		id := "meeting-m1-span-" + idea.SpanID
		_, _, err := netapi.WriteIntentFileWithID(sinkDir, id, netapi.IntentRequest{
			Type: "note", Body: idea.Body, Source: "meeting",
			CapturedAt: "2026-08-06T00:00:00Z",
		})
		return err
	}

	first, err := Resolve(context.Background(), f.bundlePath, f.writer, sink)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if first.Filed != 1 || first.MarkerFailed != 1 {
		t.Fatalf("first pass report = %+v, want filed=1 marker_failed=1", first)
	}

	second, err := Resolve(context.Background(), f.bundlePath, f.writer, sink)
	if err != nil {
		t.Fatalf("Resolve() re-run error = %v", err)
	}
	if second.Filed != 1 {
		t.Fatalf("re-run report = %+v (the span re-resolves; the sink dedupes)", second)
	}

	entries, err := os.ReadDir(sinkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("intent files after two passes = %v, want exactly one", names)
	}
}
