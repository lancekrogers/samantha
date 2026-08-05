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
	id         string
	bundlePath string
	title      string
	campaign   string
	startedAt  time.Time
	segments   *segmentStore
	pipeline   Pipeline

	mu           sync.Mutex
	writer       *meetinglog.Writer
	state        State
	lastActivity time.Time
	lastSeq      int64
	missing      []int64
	summary      meetinglog.Summary
	haveSummary  bool
	failure      string
	interrupted  bool
	finishedAt   time.Time
	done         chan struct{}
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
	status := Status{
		MeetingID: s.id, State: s.state, Bundle: s.bundlePath,
		Title: s.title, Campaign: s.campaign, StartedAt: s.startedAt,
		MissingSeqs: append([]int64(nil), s.missing...), Error: s.failure,
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
	if err := s.acceptsAudio(); err != nil {
		return err
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
	s.mu.Unlock()
	if writer == nil {
		return ErrNotRecording
	}
	if err := writer.AppendControl(kind, req.OffsetMs, req.Label, req.Text); err != nil {
		return err
	}
	s.touch(now)
	return nil
}

// acceptsAudio guards the states in which more audio is still useful.
func (s *Session) acceptsAudio() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.writer == nil:
		return ErrNotRecording
	case s.state == StateProcessing:
		return ErrProcessing
	}
	return nil
}

func (s *Session) touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = now
}

// Stop finalizes the meeting. When segments are still missing it changes
// nothing and reports the gaps, so the client can re-push and stop again;
// otherwise it hands the bundle to the pipeline on a background goroutine.
func (s *Session) Stop(ctx context.Context, lastSeq int64, now time.Time) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.writer == nil:
		return s.statusLocked(), ErrNotRecording
	case s.state == StateProcessing:
		return s.statusLocked(), ErrProcessing
	}

	missing, err := s.segments.Missing(lastSeq)
	if err != nil {
		return s.statusLocked(), err
	}
	s.missing, s.lastActivity, s.lastSeq = missing, now, lastSeq
	if len(missing) > 0 {
		return s.statusLocked(), nil
	}
	s.state, s.interrupted = StateProcessing, false
	s.done = make(chan struct{})

	// Detached from the request context: the client's HTTP call is done, but
	// transcription is not, and cancelling it would throw the meeting away.
	go s.process(context.WithoutCancel(ctx), lastSeq, true)
	return s.statusLocked(), nil
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
	highest, err := s.segments.Highest()
	if err != nil {
		highest = -1
	}
	s.mu.Lock()
	if s.writer == nil || s.state != StateInterrupted {
		s.mu.Unlock()
		return
	}
	if s.lastSeq > highest {
		highest = s.lastSeq
	}
	s.lastActivity = now
	s.done = make(chan struct{})
	s.mu.Unlock()
	// Off the sweep goroutine: transcription can take minutes and the janitor
	// still has other meetings to watch.
	go s.process(ctx, highest, true)
}

// process assembles audio, runs the pipeline, and publishes the outcome. It
// never returns an error: every failure is recorded on the session, because a
// recording that cannot be transcribed is still a recording worth keeping.
func (s *Session) process(ctx context.Context, lastSeq int64, closeBundle bool) {
	defer s.finishPass()

	audioPath := filepath.Join(s.bundlePath, meetinglog.BundleAudioName)
	gaps, err := s.assemble(ctx, audioPath, lastSeq)
	if err != nil {
		s.fail(fmt.Errorf("assemble meeting audio: %w", err))
		return
	}
	s.recordGaps(gaps, lastSeq)
	if err := s.segments.purge(); err != nil {
		s.note(err)
	}

	s.mu.Lock()
	writer, title := s.writer, s.title
	s.mu.Unlock()

	var pipelineErr error
	if s.pipeline == nil {
		pipelineErr = ErrPipelineUnavailable
	} else {
		pipelineErr = s.pipeline.Process(ctx, Job{
			BundlePath: s.bundlePath, AudioPath: audioPath, Writer: writer, Title: title,
		})
	}
	s.publish(pipelineErr, closeBundle)
}

// assemble streams the received segments into the bundle's audio.wav.
func (s *Session) assemble(ctx context.Context, audioPath string, lastSeq int64) (int64, error) {
	if lastSeq < 0 {
		return 0, nil
	}
	wav, err := audio.NewWAVWriter(audioPath, audio.SampleRate)
	if err != nil {
		return 0, err
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
func (s *Session) recordGaps(gaps, lastSeq int64) {
	if gaps <= 0 {
		return
	}
	s.mu.Lock()
	writer := s.writer
	s.mu.Unlock()
	if writer == nil {
		return
	}
	text := fmt.Sprintf("%d of %d audio segments never arrived", gaps, lastSeq+1)
	if err := writer.AppendControl(meetinglog.TypeSegmentGap, 0, "", text); err != nil {
		s.note(err)
	}
}

// publish closes the bundle (when this was a real stop) and records the
// terminal state the client will poll.
func (s *Session) publish(pipelineErr error, closeBundle bool) {
	s.mu.Lock()
	writer, interrupted := s.writer, s.interrupted
	s.mu.Unlock()

	var summary meetinglog.Summary
	var closeErr error
	if closeBundle && writer != nil {
		summary, closeErr = writer.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if closeBundle {
		s.writer = nil
		s.summary, s.haveSummary = summary, closeErr == nil
		s.finishedAt = summary.EndedAt
	}
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
		s.finishedAt = time.Now()
	}
}

// closeBundle shuts the writer without running the pipeline — serve is going
// down and a half-finished bundle beats a headless one.
func (s *Session) closeBundle() error {
	s.mu.Lock()
	writer := s.writer
	s.writer = nil
	s.mu.Unlock()
	if writer == nil {
		return nil
	}
	_, err := writer.Close()
	return err
}
