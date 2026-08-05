package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// Session is one remote-driven meeting: a bundle writer, the segment store
// feeding it, and the state machine a polling client observes.
//
// Everything mutable is behind mu. The pipeline runs on its own goroutine and
// only touches the session again to publish its outcome.
type Session struct {
	id             string
	bundlePath     string
	title          string
	campaign       string
	startedAt      time.Time
	segments       *segmentStore
	pipeline       Pipeline
	now            func() time.Time
	processTimeout time.Duration

	mu           sync.Mutex
	writer       *meetinglog.Writer
	state        State
	// finalizing serializes segment acceptance against Stop/abandon: while a
	// finalize pass drains and reconciles, new uploads are refused, and putWG
	// tracks uploads already accepted so reconciliation never runs while one
	// is mid-write (an acked segment must never miss assembly).
	finalizing bool
	lastActivity time.Time
	lastSeq      int64
	missing      []int64
	summary      meetinglog.Summary
	haveSummary  bool
	failure      string
	interrupted  bool
	finishedAt   time.Time
	done         chan struct{}
	cancelPass   context.CancelFunc
	putWG        sync.WaitGroup
	// putGate, when set by a test, runs between acceptance and the segment
	// write — the seam that makes the upload/stop interleaving deterministic.
	putGate func()
	// routes single-flights and caches route executions by capture+campaign
	// key: `camp idea notes import-meeting` does not dedupe, so a concurrent
	// double tap must share one execution, not file twice.
	routes map[string]*routeCall
}

// ID is the opaque handle the client uses in every later request.
func (s *Session) ID() string { return s.id }

// BundlePath is the .meeting directory on disk.
func (s *Session) BundlePath() string { return s.bundlePath }

// Status snapshots the session for GET /v1/meeting/{id}.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Session) statusLocked() Status {
	reported := s.missing
	if len(reported) > maxReportedMissing {
		reported = reported[:maxReportedMissing]
	}
	status := Status{
		MeetingID: s.id, State: s.state, Bundle: s.bundlePath,
		Title: s.title, Campaign: s.campaign, StartedAt: s.startedAt,
		MissingSeqs: append([]int64(nil), reported...), MissingCount: len(s.missing),
		Error: s.failure,
	}
	if s.haveSummary {
		summary := s.summary
		status.Result = &summary
	}
	return status
}

// live reports whether the bundle writer is still open — that is, whether the
// meeting can still take audio. It is also what makes a second start a 409.
func (s *Session) live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writer != nil
}

// AppendSegment stores one uploaded PCM segment. Duplicates and out-of-order
// arrivals are both fine; an interrupted meeting still accepts its tail.
func (s *Session) AppendSegment(ctx context.Context, seq int64, data []byte, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Acceptance and put registration are one atomic step: once this method
	// holds a putWG slot, any finalize pass waits for the write to land.
	s.mu.Lock()
	if err := s.acceptsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.putWG.Add(1)
	gate := s.putGate
	s.mu.Unlock()
	defer s.putWG.Done()
	if gate != nil {
		gate()
	}
	if err := s.segments.Put(seq, data); err != nil {
		return err
	}
	s.touch(now)
	return nil
}

// Control appends a client-driven event to the bundle at its own offset.
func (s *Session) Control(ctx context.Context, req ControlRequest, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	kind, ok := controlActions[req.Action]
	if !ok {
		return fmt.Errorf("%w %q", ErrBadControl, req.Action)
	}
	s.mu.Lock()
	writer := s.writer
	accept := s.acceptsLocked()
	s.mu.Unlock()
	if accept != nil {
		return accept
	}
	if err := writer.AppendControl(kind, req.OffsetMs, req.Label, req.Text); err != nil {
		return err
	}
	s.touch(now)
	return nil
}

func (s *Session) acceptsLocked() error {
	switch {
	case s.writer == nil:
		return ErrNotRecording
	case s.finalizing, s.state == StateProcessing:
		return ErrProcessing
	}
	return nil
}

// touch records activity. A client that comes back after the janitor gave up
// on it is recording again, not still interrupted — leaving the state stuck
// would show a broken meeting in the UI while audio flows in fine.
func (s *Session) touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = now
	if s.state == StateInterrupted && s.writer != nil {
		s.state, s.interrupted = StateRecording, false
	}
}

// Stop finalizes the meeting. When segments are still missing it changes
// nothing and reports the gaps, so the client can re-push and stop again;
// otherwise it hands the bundle to the pipeline on a background goroutine.
func (s *Session) Stop(ctx context.Context, lastSeq int64, now time.Time) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if lastSeq > MaxSegmentSeq {
		return Status{}, fmt.Errorf("%w: last_seq %d exceeds the %d cap", ErrBadSegment, lastSeq, MaxSegmentSeq)
	}
	// Take the finalize barrier: refuse new uploads, then wait out the ones
	// already accepted. Without this, an acked segment mid-write could be
	// missed by reconcile and purged by assembly — acknowledged audio lost.
	s.mu.Lock()
	if err := s.acceptsLocked(); err != nil {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, err
	}
	s.finalizing = true
	s.mu.Unlock()
	s.putWG.Wait()

	// Read the index off the session lock — it is disk I/O, and holding the
	// lock across it would block every status poll.
	lastSeq, missing, err := s.reconcile(lastSeq)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizing = false
	if err != nil {
		return s.statusLocked(), err
	}
	if s.writer == nil {
		return s.statusLocked(), ErrNotRecording
	}
	s.missing, s.lastActivity, s.lastSeq = missing, now, lastSeq
	if len(missing) > 0 {
		return s.statusLocked(), nil
	}
	s.state, s.interrupted = StateProcessing, false
	s.done = make(chan struct{})

	// Detached from the request context: the client's HTTP call is done, but
	// transcription is not, and cancelling it would throw the meeting away.
	go s.process(context.WithoutCancel(ctx), lastSeq)
	return s.statusLocked(), nil
}

// reconcile raises a client's last_seq to whatever actually arrived and lists
// the gaps below it. Trusting a low last_seq would be destructive: assembly
// stops there and the raw segments above it are purged, so an off-by-one or a
// restarted counter would silently delete audio the client already delivered.
func (s *Session) reconcile(lastSeq int64) (int64, []int64, error) {
	highest, err := s.segments.Highest()
	if err != nil {
		return 0, nil, err
	}
	if highest > lastSeq {
		lastSeq = highest
	}
	missing, err := s.segments.Missing(lastSeq)
	if err != nil {
		return 0, nil, err
	}
	return lastSeq, missing, nil
}

// Done reports the channel closed when the current processing pass ends, or
// nil when nothing is running. Tests wait on it instead of polling.
func (s *Session) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// markInterrupted is the janitor's first move: the client went quiet, so
// freeze what we have and stop pretending the meeting is healthy. The writer
// stays open because a phone that comes back may still push its tail.
func (s *Session) markInterrupted(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer == nil || s.state != StateRecording {
		return
	}
	s.state, s.interrupted, s.lastActivity = StateInterrupted, true, now
}

// abandon is the janitor's second move: nobody came back, so process whatever
// audio exists and close the bundle. The state stays interrupted — the results
// are real, but the client should know they may be short a tail.
func (s *Session) abandon(ctx context.Context, now time.Time) {
	s.mu.Lock()
	if s.writer == nil || s.state != StateInterrupted {
		s.mu.Unlock()
		return
	}
	// Claim the meeting before releasing the lock: a client that reconnects
	// mid-abandon must get a 409, not a second concurrent finalize. The
	// interrupted flag survives, so publish still lands on interrupted.
	s.state = StateProcessing
	s.lastActivity = now
	s.done = make(chan struct{})
	lastSeq := s.lastSeq
	s.mu.Unlock()

	// Same barrier as Stop: an upload accepted just before the claim must
	// land before the tail is measured, or its audio is silently dropped.
	s.putWG.Wait()
	highest, err := s.segments.Highest()
	if err != nil {
		highest = -1
	}
	if lastSeq > highest {
		highest = lastSeq
	}
	// Off the sweep goroutine, and detached from it: transcription takes
	// minutes, the janitor has other meetings to watch, and a sweep returning
	// must not cancel the pass it just started.
	go s.process(context.WithoutCancel(ctx), highest)
}

// process assembles audio, runs the pipeline, and publishes the outcome. It
// never returns an error: every failure is recorded on the session, because a
// recording that cannot be transcribed is still a recording worth keeping.
func (s *Session) process(parent context.Context, lastSeq int64) {
	defer s.finishPass()

	// A pass is bounded: without a deadline a wedged model would hold the
	// single meeting slot for the life of the serve process.
	ctx, cancel := context.WithTimeout(parent, s.processTimeout)
	defer cancel()
	s.setCancel(cancel)

	audioPath := filepath.Join(s.bundlePath, meetinglog.BundleAudioName)
	gaps, err := s.assemble(ctx, audioPath, lastSeq)
	if err != nil {
		s.fail(fmt.Errorf("assemble meeting audio: %w", err))
		return
	}
	s.recordGaps(gaps)
	// Safe to drop the raw segments now: assembly covered every sequence on
	// disk (Stop and abandon both raise lastSeq to the highest received), so
	// audio.wav holds the same PCM and a failed pipeline is re-runnable.
	if err := s.segments.purge(); err != nil {
		s.note(err)
	}

	s.mu.Lock()
	writer, title := s.writer, s.title
	s.mu.Unlock()
	if writer == nil {
		// The bundle was closed underneath us — serve is shutting down. Say so
		// instead of handing the pipeline a nil writer and panicking.
		s.fail(ErrNotRecording)
		return
	}

	var pipelineErr error
	if s.pipeline == nil {
		pipelineErr = ErrPipelineUnavailable
	} else {
		pipelineErr = s.pipeline.Process(ctx, Job{
			MeetingID: s.id, BundlePath: s.bundlePath,
			AudioPath: audioPath, Writer: writer, Title: title,
		})
	}
	s.publish(pipelineErr)
}

// setCancel publishes the running pass's cancel func so shutdown can stop it.
func (s *Session) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelPass = cancel
}

// assemble streams the received segments into the bundle's audio.wav.
func (s *Session) assemble(ctx context.Context, audioPath string, lastSeq int64) ([]Gap, error) {
	if lastSeq < 0 {
		return nil, nil
	}
	wav, err := audio.NewWAVWriter(audioPath, audio.SampleRate)
	if err != nil {
		return nil, err
	}
	gaps, assembleErr := s.segments.Assemble(ctx, wav, lastSeq)
	closeErr := wav.Close()
	if assembleErr != nil {
		return gaps, assembleErr
	}
	return gaps, closeErr
}

// recordGaps leaves a marker in the bundle for audio that never arrived, so a
// later reader never mistakes dropped segments for silence.
func (s *Session) recordGaps(gaps []Gap) {
	if len(gaps) == 0 {
		return
	}
	s.mu.Lock()
	writer := s.writer
	s.mu.Unlock()
	if writer == nil {
		return
	}
	for _, gap := range gaps {
		text := fmt.Sprintf("%d audio segment(s) never arrived; %dms of silence stands in",
			gap.Segments, gap.DurationMs)
		if err := writer.AppendControl(meetinglog.TypeSegmentGap, gap.OffsetMs, "", text); err != nil {
			s.note(err)
			return
		}
	}
}

// publish closes the bundle (when this was a real stop) and records the
// terminal state the client will poll.
func (s *Session) publish(pipelineErr error) {
	s.mu.Lock()
	writer, interrupted := s.writer, s.interrupted
	s.mu.Unlock()

	var summary meetinglog.Summary
	var closeErr error
	if writer != nil {
		summary, closeErr = writer.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer = nil
	s.summary, s.haveSummary = summary, closeErr == nil
	s.finishedAt = s.now()
	switch {
	case pipelineErr != nil:
		s.state, s.failure = StateFailed, pipelineErr.Error()
	case closeErr != nil:
		s.state, s.failure = StateFailed, closeErr.Error()
	case interrupted:
		s.state = StateInterrupted
	default:
		s.state = StateReady
	}
}

// fail records a pre-pipeline failure without discarding anything on disk.
func (s *Session) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.failure = StateFailed, err.Error()
}

// note appends a non-fatal problem to the session's error text: the meeting
// still succeeded, but the client should be able to see what went sideways.
func (s *Session) note(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == "" {
		s.failure = err.Error()
		return
	}
	s.failure += "; " + err.Error()
}

func (s *Session) finishPass() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	if s.finishedAt.IsZero() && s.writer == nil {
		s.finishedAt = s.now()
	}
}

// closeBundle shuts the writer without running the pipeline — serve is going
// down and a half-finished bundle beats a headless one.
func (s *Session) closeBundle() error {
	s.mu.Lock()
	writer, cancel := s.writer, s.cancelPass
	s.writer, s.cancelPass = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if writer == nil {
		return nil
	}
	_, err := writer.Close()
	return err
}

// Summary returns the finished meeting's summary for routing. Ready meetings
// route normally; janitor-processed interrupted meetings do too — their notes
// are just as real, only the ending wasn't clean. Everything else is not
// routable yet (or ever, for failed ones — reprocess on the Mac first).
func (s *Session) Summary() (meetinglog.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	routable := s.haveSummary && (s.state == StateReady || s.state == StateInterrupted)
	if !routable {
		return meetinglog.Summary{}, ErrNotRoutable
	}
	return s.summary, nil
}

// routeCall is one route execution for a key: concurrent callers wait on
// done and share the outcome.
type routeCall struct {
	done    chan struct{}
	receipt RouteReceipt
	err     error
}

// RouteOnce runs fn at most once per key, concurrently-safe: while one call
// is in flight every duplicate waits and shares its outcome, and a success is
// cached for the session's lifetime. The importer does not dedupe, so this is
// the whole idempotency story — check-then-execute as separate steps would
// let a double tap file the meeting twice. A failed call is forgotten so the
// next attempt retries for real.
//
// Keys are the caller's business; the handler keys by normalized capture mode
// plus campaign so `capture: intent` after a meeting-import is a new route,
// not a cache hit.
func (s *Session) RouteOnce(key string, fn func() (RouteReceipt, error)) (RouteReceipt, error) {
	s.mu.Lock()
	if call, ok := s.routes[key]; ok {
		s.mu.Unlock()
		<-call.done
		return call.receipt, call.err
	}
	call := &routeCall{done: make(chan struct{})}
	if s.routes == nil {
		s.routes = make(map[string]*routeCall)
	}
	s.routes[key] = call
	s.mu.Unlock()

	call.receipt, call.err = fn()
	close(call.done)
	if call.err != nil {
		s.mu.Lock()
		delete(s.routes, key)
		s.mu.Unlock()
	}
	return call.receipt, call.err
}
