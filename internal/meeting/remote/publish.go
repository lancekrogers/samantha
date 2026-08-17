package remote

import (
	"context"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// The end of a meeting: close the bundle, commit the terminal state a client
// polls, and file the notes where the start request planned. Delivery lives
// here rather than in the manager because only the session knows when its
// recording actually became notes.

// publish closes the bundle (when this was a real stop) and records the
// terminal state the client will poll, then files the start-time route plan.
// Delivery runs after the bundle is closed because the note is rendered from
// the finished event stream — session_end included.
func (s *Session) publish(pipelineErr error) {
	s.mu.Lock()
	writer, interrupted := s.writer, s.interrupted
	s.mu.Unlock()

	var summary meetinglog.Summary
	var closeErr error
	if writer != nil {
		summary, closeErr = writer.Close()
	}

	if s.recordOutcome(summary, closeErr, pipelineErr, interrupted) == StateReady {
		s.deliverPlan(summary)
	}
}

// recordOutcome commits the terminal state and returns it.
func (s *Session) recordOutcome(summary meetinglog.Summary, closeErr, pipelineErr error, interrupted bool) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer = nil
	s.step = ""
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
	return s.state
}

// deliverPlan files the meeting where its start request planned, exactly once,
// through the same RouteOnce gate a manual route takes — so a user tapping
// "File notes" while the plan is in flight shares that receipt instead of
// filing the notes twice.
//
// A delivery failure is deliberately not raised on Status: the durable record
// is the route_failed event RouteByID appends, which is what makes the bundle
// visibly failed in the meeting index and retryable by `meeting sweep`. An
// earlier failure (nothing rendered, nothing attempted) leaves the plan
// undelivered, which the same sweep picks up.
func (s *Session) deliverPlan(summary meetinglog.Summary) {
	s.mu.Lock()
	plan, deliver := s.plan, s.deliverPlanFn
	s.mu.Unlock()
	if plan.destID == "" || deliver == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), routePlanTimeout)
	defer cancel()
	_, _ = s.RouteOnce(plan.key(), func() (RouteReceipt, error) {
		return deliver(ctx, summary, plan.destID, plan.body)
	})
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
