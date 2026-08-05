package remote

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// pcm builds one segment of samples with a recognizable value per sequence so
// assembly order is checkable, not just assembly length.
func pcm(value int16, samples int) []byte {
	raw := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(value))
	}
	return raw
}

// recordingPipeline captures what the manager handed it and can fail on cue.
type recordingPipeline struct {
	err    error
	jobs   chan Job
	before func(Job)
}

func newRecordingPipeline(err error) *recordingPipeline {
	return &recordingPipeline{err: err, jobs: make(chan Job, 4)}
}

func (p *recordingPipeline) Process(ctx context.Context, job Job) error {
	if p.before != nil {
		p.before(job)
	}
	p.jobs <- job
	return p.err
}

func testManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	m, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return m
}

func startSession(t *testing.T, m *Manager) *Session {
	t.Helper()
	session, err := m.Start(context.Background(), StartRequest{Title: "Standup"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return session
}

// waitDone blocks until the current processing pass finishes.
func waitDone(t *testing.T, session *Session) {
	t.Helper()
	done := session.Done()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processing did not finish")
	}
}

func TestStartRejectsSecondLiveMeeting(t *testing.T) {
	m := testManager(t, Options{})
	first := startSession(t, m)

	_, err := m.Start(context.Background(), StartRequest{Title: "Other"})
	if !errors.Is(err, ErrMeetingActive) {
		t.Fatalf("second Start() error = %v, want ErrMeetingActive", err)
	}

	// Once the first meeting is finalized, a new one is allowed.
	if _, err := first.Stop(context.Background(), -1, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, first)
	if _, err := m.Start(context.Background(), StartRequest{Title: "Other"}); err != nil {
		t.Fatalf("Start() after finalize error = %v", err)
	}
}

func TestStartCancelledContext(t *testing.T) {
	m := testManager(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Start(ctx, StartRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
}

func TestSessionLookupErrors(t *testing.T) {
	m := testManager(t, Options{})
	if _, err := m.Session("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Session() error = %v, want ErrNotFound", err)
	}
}

func TestStopReportsMissingSegmentsThenFinalizes(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	m := testManager(t, Options{Pipeline: pipe})
	session := startSession(t, m)
	now := time.Now()

	for _, seq := range []int64{0, 2} {
		if err := session.AppendSegment(context.Background(), seq, pcm(int16(seq+1), 8), now); err != nil {
			t.Fatalf("AppendSegment(%d) error = %v", seq, err)
		}
	}

	status, err := session.Stop(context.Background(), 2, now)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if len(status.MissingSeqs) != 1 || status.MissingSeqs[0] != 1 {
		t.Fatalf("MissingSeqs = %v, want [1]", status.MissingSeqs)
	}
	if status.State != StateRecording {
		t.Fatalf("State = %q, want recording — a gap must not finalize", status.State)
	}

	if err := session.AppendSegment(context.Background(), 1, pcm(2, 8), now); err != nil {
		t.Fatalf("re-push error = %v", err)
	}
	status, err = session.Stop(context.Background(), 2, now)
	if err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if len(status.MissingSeqs) != 0 || status.State != StateProcessing {
		t.Fatalf("status = %+v, want processing with no gaps", status)
	}
	waitDone(t, session)

	if got := session.Status().State; got != StateReady {
		t.Fatalf("final state = %q, want ready", got)
	}
	select {
	case job := <-pipe.jobs:
		if job.BundlePath != session.BundlePath() {
			t.Errorf("job bundle = %q, want %q", job.BundlePath, session.BundlePath())
		}
		if filepath.Base(job.AudioPath) != meetinglog.BundleAudioName {
			t.Errorf("job audio = %q, want %s", job.AudioPath, meetinglog.BundleAudioName)
		}
	default:
		t.Fatal("pipeline was never invoked")
	}
}

func TestStopFailuresKeepTheRecording(t *testing.T) {
	tests := []struct {
		name      string
		pipeline  Pipeline
		wantState State
		wantErr   string
	}{
		{
			name:      "pipeline failure is reported, bundle survives",
			pipeline:  newRecordingPipeline(errors.New("stt exploded")),
			wantState: StateFailed,
			wantErr:   "stt exploded",
		},
		{
			name:      "no pipeline configured",
			pipeline:  nil,
			wantState: StateFailed,
			wantErr:   ErrPipelineUnavailable.Error(),
		},
		{
			name:      "healthy pipeline",
			pipeline:  newRecordingPipeline(nil),
			wantState: StateReady,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t, Options{Pipeline: tt.pipeline})
			session := startSession(t, m)
			now := time.Now()
			if err := session.AppendSegment(context.Background(), 0, pcm(1, 16), now); err != nil {
				t.Fatalf("AppendSegment() error = %v", err)
			}
			if _, err := session.Stop(context.Background(), 0, now); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			waitDone(t, session)

			status := session.Status()
			if status.State != tt.wantState {
				t.Fatalf("state = %q, want %q (error %q)", status.State, tt.wantState, status.Error)
			}
			if tt.wantErr != "" && !strings.Contains(status.Error, tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", status.Error, tt.wantErr)
			}
			// The recording itself is never the casualty of a failed pipeline.
			if _, err := os.Stat(filepath.Join(session.BundlePath(), meetinglog.BundleAudioName)); err != nil {
				t.Errorf("assembled audio missing after %s: %v", tt.name, err)
			}
		})
	}
}

func TestStopRejectsRepeatCalls(t *testing.T) {
	release := make(chan struct{})
	pipe := newRecordingPipeline(nil)
	pipe.before = func(Job) { <-release }
	m := testManager(t, Options{Pipeline: pipe})
	session := startSession(t, m)
	now := time.Now()
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 16), now); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 0, now); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if _, err := session.Stop(context.Background(), 0, now); !errors.Is(err, ErrProcessing) {
		t.Fatalf("Stop() while processing = %v, want ErrProcessing", err)
	}
	if err := session.AppendSegment(context.Background(), 1, pcm(1, 16), now); !errors.Is(err, ErrProcessing) {
		t.Fatalf("AppendSegment() while processing = %v, want ErrProcessing", err)
	}
	close(release)
	waitDone(t, session)

	if _, err := session.Stop(context.Background(), 0, now); !errors.Is(err, ErrNotRecording) {
		t.Fatalf("Stop() after finalize = %v, want ErrNotRecording", err)
	}
}

func TestStopCancelledContext(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Stop(ctx, 0, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context.Canceled", err)
	}
	if err := session.AppendSegment(ctx, 0, pcm(1, 8), time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendSegment() error = %v, want context.Canceled", err)
	}
	if err := session.Control(ctx, ControlRequest{Action: "bookmark"}, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Control() error = %v, want context.Canceled", err)
	}
}

// TestBundleLayoutMatchesDesktop is the anti-drift guard: a phone-started
// meeting must be indistinguishable on disk from a desktop recording, because
// routing, rendering, and reprocessing all read the same layout.
func TestBundleLayoutMatchesDesktop(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	now := time.Now()
	if err := session.AppendSegment(context.Background(), 0, pcm(1234, 160), now); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 0, now); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)

	bundle := session.BundlePath()
	if !strings.HasSuffix(bundle, ".meeting") {
		t.Errorf("bundle %q does not carry the .meeting suffix", bundle)
	}
	for _, rel := range []string{
		meetinglog.BundleDocumentName,
		filepath.Join(meetinglog.BundleInternalDirName, meetinglog.BundleEventsName),
		meetinglog.BundleAudioName,
	} {
		if _, err := os.Stat(filepath.Join(bundle, rel)); err != nil {
			t.Errorf("bundle is missing %s: %v", rel, err)
		}
	}

	// The desktop reference bundle for the same inputs.
	desktop := filepath.Join(t.TempDir(), "desktop.meeting")
	writer, err := meetinglog.CreateBundle(desktop, "Standup", "remote")
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got, want := eventTypes(t, bundle), eventTypes(t, desktop); !equalStrings(got, want) {
		t.Errorf("event types = %v, want the desktop sequence %v", got, want)
	}

	samples, rate, err := audio.ReadWAVFloat32(filepath.Join(bundle, meetinglog.BundleAudioName))
	if err != nil {
		t.Fatalf("ReadWAVFloat32() error = %v", err)
	}
	if rate != audio.SampleRate {
		t.Errorf("sample rate = %d, want %d", rate, audio.SampleRate)
	}
	if len(samples) != 160 {
		t.Errorf("assembled %d samples, want 160", len(samples))
	}
}

func eventTypes(t *testing.T, bundle string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(bundle, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var event meetinglog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		types = append(types, event.Type)
	}
	return types
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
