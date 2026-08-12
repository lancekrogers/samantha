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

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
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
	return &recordingPipeline{err: err, jobs: make(chan Job, 8)}
}

func (p *recordingPipeline) Process(ctx context.Context, job Job) error {
	if p.before != nil {
		p.before(job)
	}
	// Never block the session on a test that is not reading: a stuck pipeline
	// would look like a product bug rather than a full channel.
	select {
	case p.jobs <- job:
	default:
	}
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

// TestStopNeverDiscardsDeliveredAudio is the property the whole design exists
// for. A client that reports a last_seq below what it actually delivered — an
// off-by-one, a restarted counter — must not cause the extra audio to be
// assembled away and then purged.
func TestStopNeverDiscardsDeliveredAudio(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	now := time.Now()
	const segments, samplesEach = 5, 64
	for seq := int64(0); seq < segments; seq++ {
		if err := session.AppendSegment(context.Background(), seq, pcm(int16(seq+1), samplesEach), now); err != nil {
			t.Fatal(err)
		}
	}

	// The client under-reports by four segments.
	if _, err := session.Stop(context.Background(), 0, now); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)

	if got := session.Status().State; got != StateReady {
		t.Fatalf("state = %q, want ready", got)
	}
	samples, _, err := audio.ReadWAVFloat32(filepath.Join(session.BundlePath(), meetinglog.BundleAudioName))
	if err != nil {
		t.Fatalf("ReadWAVFloat32() error = %v", err)
	}
	if want := segments * samplesEach; len(samples) != want {
		t.Fatalf("assembled %d samples, want %d — audio the client delivered was thrown away", len(samples), want)
	}
}

// TestStopWithNoAudioIsNotReported as ready-with-nothing: an empty meeting
// still closes cleanly, but it must not claim results it does not have.
func TestStopWithNoSegments(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	if _, err := session.Stop(context.Background(), -1, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)
	if got := session.Status().State; got != StateReady {
		t.Fatalf("state = %q, want ready", got)
	}
	if _, err := os.Stat(filepath.Join(session.BundlePath(), meetinglog.BundleAudioName)); err == nil {
		t.Error("a meeting with no audio produced an audio.wav")
	}
}

func TestStopRejectsAbsurdLastSeq(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	done := make(chan error, 1)
	go func() {
		_, err := session.Stop(context.Background(), 1<<40, time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBadSegment) {
			t.Fatalf("Stop() error = %v, want ErrBadSegment", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() with an absurd last_seq did not return — the session lock is wedged")
	}
	// The session is untouched and still usable.
	if got := session.Status().State; got != StateRecording {
		t.Errorf("state = %q, want recording", got)
	}
}

func TestAppendSegmentRejectsAbsurdSequence(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	err := session.AppendSegment(context.Background(), 1<<40, pcm(1, 8), time.Now())
	if !errors.Is(err, ErrBadSegment) {
		t.Fatalf("AppendSegment() error = %v, want ErrBadSegment", err)
	}
	// The meeting slot is not wedged: a normal stop still finishes.
	if _, err := session.Stop(context.Background(), -1, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)
	if session.live() {
		t.Error("session still holds the meeting slot")
	}
}

// TestManagerCloseDuringProcessingDoesNotPanic covers Ctrl-C on serve while a
// meeting is transcribing: the bundle is closed underneath the pipeline, which
// must be told rather than handed a nil writer.
func TestManagerCloseDuringProcessingDoesNotPanic(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	pipe := newRecordingPipeline(nil)
	pipe.before = func(job Job) {
		close(entered)
		<-release
		if job.Writer == nil {
			t.Error("pipeline was handed a nil writer")
		}
	}
	m := testManager(t, Options{Pipeline: pipe})
	session := startSession(t, m)
	now := time.Now()
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 32), now); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), 0, now); err != nil {
		t.Fatal(err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline never started")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(release)
	waitDone(t, session)

	// Whatever the outcome, serve survived and the bundle is closed.
	if session.live() {
		t.Error("Close() left the bundle open")
	}
}

// TestProcessWithClosedBundleFailsCleanly is the same hazard without the
// timing race: a pass that finds its writer gone reports it instead of
// dereferencing nil.
func TestProcessWithClosedBundleFailsCleanly(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	m := testManager(t, Options{Pipeline: pipe})
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 32), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := session.closeBundle(); err != nil {
		t.Fatal(err)
	}

	session.done = make(chan struct{})
	session.process(context.Background(), 0)

	if got := session.Status().State; got != StateFailed {
		t.Fatalf("state = %q, want failed", got)
	}
	if len(pipe.jobs) != 0 {
		t.Error("the pipeline ran against a closed bundle")
	}
}
