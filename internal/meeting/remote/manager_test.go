package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// fakeClock drives the janitor without sleeping: every timeout in this file is
// a decision about elapsed time, not about wall time.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func janitorManager(t *testing.T, pipe Pipeline) (*Manager, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}
	m := testManager(t, Options{
		Pipeline:       pipe,
		StallTimeout:   5 * time.Minute,
		AbandonTimeout: 5 * time.Minute,
		Now:            clock.Now,
	})
	return m, clock
}

func TestJanitorMarksStalledRecordingInterrupted(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)

	clock.advance(4 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	if got := session.Status().State; got != StateRecording {
		t.Fatalf("state after 4 minutes = %q, want recording", got)
	}

	clock.advance(time.Minute)
	m.Sweep(context.Background(), clock.now)
	if got := session.Status().State; got != StateInterrupted {
		t.Fatalf("state after the stall window = %q, want interrupted", got)
	}
	// The bundle is still open, so the client's tail is still welcome.
	if !session.live() {
		t.Error("an interrupted meeting closed its bundle too early")
	}
}

func TestJanitorTimerResetsOnActivity(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)

	for i := 0; i < 3; i++ {
		clock.advance(4 * time.Minute)
		if err := session.AppendSegment(context.Background(), int64(i), pcm(1, 8), clock.now); err != nil {
			t.Fatalf("AppendSegment() error = %v", err)
		}
		m.Sweep(context.Background(), clock.now)
		if got := session.Status().State; got != StateRecording {
			t.Fatalf("state = %q after activity, want recording", got)
		}
	}

	// A control event is activity too — a paused meeting is not a dead one.
	clock.advance(4 * time.Minute)
	if err := session.Control(context.Background(), ControlRequest{Action: "pause", OffsetMs: 1000}, clock.now); err != nil {
		t.Fatalf("Control() error = %v", err)
	}
	clock.advance(4 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	if got := session.Status().State; got != StateRecording {
		t.Fatalf("state = %q, want recording", got)
	}
}

// TestInterruptedMeetingAcceptsTailThenFinalizes is the reconnect story: the
// phone comes back inside the grace window, pushes what it held, and the
// meeting completes normally.
func TestInterruptedMeetingAcceptsTailThenFinalizes(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	m, clock := janitorManager(t, pipe)
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 16), clock.now); err != nil {
		t.Fatal(err)
	}

	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	if got := session.Status().State; got != StateInterrupted {
		t.Fatalf("state = %q, want interrupted", got)
	}

	if err := session.AppendSegment(context.Background(), 1, pcm(2, 16), clock.now); err != nil {
		t.Fatalf("tail re-push error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 1, clock.now); err != nil {
		t.Fatalf("Stop() after reconnect error = %v", err)
	}
	waitDone(t, session)

	status := session.Status()
	if status.State != StateReady {
		t.Fatalf("state = %q, want ready (error %q)", status.State, status.Error)
	}
	if len(pipe.jobs) != 1 {
		t.Errorf("pipeline ran %d times, want once", len(pipe.jobs))
	}
}

// TestJanitorAbandonsAfterGracePeriod is the phone-never-came-back story: the
// partial recording is processed rather than lost, and the state keeps saying
// interrupted so nobody mistakes it for a complete meeting.
func TestJanitorAbandonsAfterGracePeriod(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	m, clock := janitorManager(t, pipe)
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(7, 64), clock.now); err != nil {
		t.Fatal(err)
	}

	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	waitDone(t, session)

	status := session.Status()
	if status.State != StateInterrupted {
		t.Fatalf("state = %q, want interrupted (error %q)", status.State, status.Error)
	}
	if status.Result == nil {
		t.Error("an abandoned meeting produced no summary")
	}
	if len(pipe.jobs) != 1 {
		t.Fatalf("pipeline ran %d times, want once", len(pipe.jobs))
	}
	// Partial audio survived.
	info, err := os.Stat(filepath.Join(session.BundlePath(), meetinglog.BundleAudioName))
	if err != nil {
		t.Fatalf("assembled audio missing: %v", err)
	}
	if info.Size() <= 44 {
		t.Errorf("audio.wav is %d bytes — header only, no partial recording", info.Size())
	}
	// The slot is free again once the bundle is closed.
	if _, err := m.Start(context.Background(), StartRequest{Title: "Next"}); err != nil {
		t.Errorf("Start() after abandon error = %v", err)
	}
}

func TestSweepStopsOnCancelledContext(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	clock.advance(10 * time.Minute)
	m.Sweep(ctx, clock.now)
	if got := session.Status().State; got != StateRecording {
		t.Fatalf("state = %q, want an untouched recording — a cancelled sweep must do nothing", got)
	}
}

func TestRunJanitorExitsWithContext(t *testing.T) {
	m := testManager(t, Options{SweepInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		m.RunJanitor(ctx)
		close(stopped)
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("RunJanitor did not exit when its context was cancelled")
	}
}

func TestSweepForgetsFinishedMeetings(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	clock := &fakeClock{now: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}
	m := testManager(t, Options{Pipeline: pipe, Retention: time.Hour, Now: clock.Now})
	session := startSession(t, m)
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)

	clock.advance(30 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	if _, err := m.Session(session.ID()); err != nil {
		t.Fatalf("meeting forgotten inside its retention window: %v", err)
	}

	clock.advance(2 * time.Hour)
	m.Sweep(context.Background(), clock.now)
	if _, err := m.Session(session.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Session() after retention = %v, want ErrNotFound", err)
	}
}

func TestManagerCloseShutsOpenBundles(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 8), clock.now); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if session.live() {
		t.Error("Close() left a bundle open")
	}
	types := eventTypes(t, session.BundlePath())
	if types[len(types)-1] != meetinglog.TypeSessionEnd {
		t.Errorf("bundle events end with %q, want %q", types[len(types)-1], meetinglog.TypeSessionEnd)
	}
}

// TestControlEventsUseTheDesktopSchema is the anti-drift guard for control
// events: the same Event fields a desktop recording writes, at the offset the
// client reported rather than the one the network happened to deliver.
func TestControlEventsUseTheDesktopSchema(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)

	controls := []ControlRequest{
		{Action: "pause", OffsetMs: 1000},
		{Action: "resume", OffsetMs: 2000},
		{Action: "bookmark", OffsetMs: 3000, Label: "decision", Text: "ship it"},
		{Action: "idea_start", OffsetMs: 4000},
		{Action: "idea_end", OffsetMs: 6000, Text: "file a bug about pairing"},
	}
	for _, control := range controls {
		if err := session.Control(context.Background(), control, clock.now); err != nil {
			t.Fatalf("Control(%s) error = %v", control.Action, err)
		}
	}

	events := decodeEvents(t, session.BundlePath())
	byType := make(map[string]meetinglog.Event, len(events))
	for _, event := range events {
		byType[event.Type] = event
	}
	for _, control := range controls {
		event, ok := byType[controlActions[control.Action]]
		if !ok {
			t.Fatalf("no %q event in the bundle", control.Action)
		}
		if event.OffsetMs != control.OffsetMs {
			t.Errorf("%s offset_ms = %d, want the client's %d", control.Action, event.OffsetMs, control.OffsetMs)
		}
		if event.TS == "" {
			t.Errorf("%s event has no timestamp", control.Action)
		}
	}
	if got := byType[meetinglog.TypeBookmark]; got.Label != "decision" || got.Text != "ship it" {
		t.Errorf("bookmark = %+v, want label/text preserved", got)
	}

	// The human document carries the moment too, in the recorder's format.
	doc, err := os.ReadFile(filepath.Join(session.BundlePath(), meetinglog.BundleDocumentName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "ship it") {
		t.Error("meeting.md does not mention the bookmark caption")
	}
}

func TestControlRejectsUnknownActionAndClosedBundle(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)

	err := session.Control(context.Background(), ControlRequest{Action: "explode"}, clock.now)
	if !errors.Is(err, ErrBadControl) {
		t.Fatalf("Control() error = %v, want ErrBadControl", err)
	}
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	err = session.Control(context.Background(), ControlRequest{Action: "bookmark"}, clock.now)
	if !errors.Is(err, ErrNotRecording) {
		t.Fatalf("Control() after stop = %v, want ErrNotRecording", err)
	}
}

// TestGapsAreRecordedInTheBundle proves dropped audio is stated, not silently
// smoothed over: an outbox that overflowed leaves a mark a reader can see.
func TestGapsAreRecordedInTheBundle(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	m, clock := janitorManager(t, pipe)
	session := startSession(t, m)
	for _, seq := range []int64{0, 3} {
		if err := session.AppendSegment(context.Background(), seq, pcm(1, 16), clock.now); err != nil {
			t.Fatal(err)
		}
	}
	// The janitor's abandon path processes what arrived, gaps and all.
	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now)
	waitDone(t, session)

	var gap *meetinglog.Event
	for _, event := range decodeEvents(t, session.BundlePath()) {
		if event.Type == meetinglog.TypeSegmentGap {
			e := event
			gap = &e
		}
	}
	if gap == nil {
		t.Fatal("no segment_gap event for the two segments that never arrived")
	}
	if !strings.Contains(gap.Text, "2 of 4") {
		t.Errorf("gap text = %q, want it to count the missing segments", gap.Text)
	}
}

func decodeEvents(t *testing.T, bundle string) []meetinglog.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(bundle, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var events []meetinglog.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var event meetinglog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"empty falls back", "", "Meeting"},
		{"whitespace only falls back", "   \t ", "Meeting"},
		{"kept as written", "Standup", "Standup"},
		{"inner whitespace collapses", "Q3  planning\treview", "Q3 planning review"},
		{"newlines cannot forge header lines", "Standup\n# Started: 1999", "Standup # Started: 1999"},
		{"over-long titles are capped", strings.Repeat("a", maxTitleRunes+40), strings.Repeat("a", maxTitleRunes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTitle(tt.title); got != tt.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

// TestStartSurvivesBundleNameCollision covers stop-then-start inside one
// second: bundle names are second-resolution, so the second meeting must get
// its own directory instead of a 500.
func TestStartSurvivesBundleNameCollision(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	first := startSession(t, m)
	if _, err := first.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, first)

	// Same fake clock, so BundleName produces the same candidate path.
	second, err := m.Start(context.Background(), StartRequest{Title: "Standup"})
	if err != nil {
		t.Fatalf("Start() after a same-second meeting error = %v", err)
	}
	if second.BundlePath() == first.BundlePath() {
		t.Fatal("the second meeting reused the first bundle directory")
	}
	if !strings.HasSuffix(second.BundlePath(), meeting.BundleSuffix) {
		t.Errorf("bundle %q lost its suffix", second.BundlePath())
	}
	if _, err := os.Stat(filepath.Join(second.BundlePath(), meetinglog.BundleDocumentName)); err != nil {
		t.Errorf("second bundle is missing its document: %v", err)
	}
}

func TestControlRejectedWhileProcessing(t *testing.T) {
	release := make(chan struct{})
	pipe := newRecordingPipeline(nil)
	pipe.before = func(Job) { <-release }
	m, clock := janitorManager(t, pipe)
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 16), clock.now); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), 0, clock.now); err != nil {
		t.Fatal(err)
	}
	err := session.Control(context.Background(), ControlRequest{Action: "bookmark"}, clock.now)
	if !errors.Is(err, ErrProcessing) {
		t.Fatalf("Control() while processing = %v, want ErrProcessing", err)
	}
	close(release)
	waitDone(t, session)
}
