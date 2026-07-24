//go:build !integration

// Package listen provides a continuous STT loop: run sessions back to back
// against a provider and dispatch a callback per utterance until stopped. It
// never touches Brain, TTS, or the pipeline — it is the reusable primitive
// behind `samantha meeting record` and any future dictation or serve-mode
// text bridge.
package listen

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lancekrogers/samantha/internal/stt"
)

// Retry constants mirror the conversational loop's proven values
// (internal/app): three consecutive failures give up; a short backoff
// separates retries. Unlike internal/app there is no fall-back-to-text
// branch — the recorder either listens or exits.
const (
	maxConsecutiveFailures = 3
	retryBackoff           = 500 * time.Millisecond
)

// Loop repeatedly runs STT sessions against provider and dispatches events to
// sink until ctx is cancelled or consecutive failures exceed the threshold.
// Each iteration mirrors pipeline.transcribeTurn's session shape: reset the
// capture ring, start a session, drain its events, close it.
//
// Return contract: nil when ctx was cancelled (clean stop); non-nil only when
// the consecutive-failure threshold was exceeded.
func Loop(ctx context.Context, capture Resetter, provider stt.Provider, sink Sink) error {
	return LoopWithHooks(ctx, capture, provider, sink, Hooks{})
}

// LoopWithHooks is Loop plus optional phase/level/partial hooks for TUI meters.
func LoopWithHooks(ctx context.Context, capture Resetter, provider stt.Provider, sink Sink, hooks Hooks) error {
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		capture.Reset()

		session, err := provider.Start(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			failures++
			if sinkErr := sink.OnError(fmt.Errorf("start STT session: %w", err)); sinkErr != nil {
				return fmt.Errorf("listen sink: %w", sinkErr)
			}
			if failures >= maxConsecutiveFailures {
				return fmt.Errorf("listen: %d consecutive STT failures, giving up: %w", failures, err)
			}
			if !sleepCtx(ctx, retryBackoff) {
				return nil
			}
			continue
		}

		failed, sinkErr := drainSession(ctx, session, sink, hooks, &failures)
		_ = session.Close()
		if sinkErr != nil {
			return fmt.Errorf("listen sink: %w", sinkErr)
		}

		if failed && failures >= maxConsecutiveFailures {
			return fmt.Errorf("listen: %d consecutive STT failures, giving up", failures)
		}
		if failed && !sleepCtx(ctx, retryBackoff) {
			return nil
		}
	}
}

// drainSession consumes one session's events until its channel closes,
// reporting whether the session ended in failure. A final transcript or a
// clean timeout resets the consecutive-failure counter.
func drainSession(ctx context.Context, session stt.Session, sink Sink, hooks Hooks, failures *int) (failed bool, sinkErr error) {
	for event := range session.Events() {
		// InputLevel is droppable UI meter data — handle before typed switch.
		if lvl, ok := event.(stt.InputLevel); ok {
			if hooks.OnLevel != nil {
				hooks.OnLevel(lvl.Level)
			}
			continue
		}

		te := stt.ToTyped(event)
		switch te.Kind {
		case stt.KindPhase:
			if hooks.OnPhase != nil {
				hooks.OnPhase(te.Phase)
			}
		case stt.KindPartialTranscript:
			if te.Text != "" && hooks.OnPartial != nil {
				hooks.OnPartial(te.Text)
			}
		case stt.KindInputLevel:
			if hooks.OnLevel != nil {
				hooks.OnLevel(te.Level)
			}
		case stt.KindFinalTranscript:
			*failures = 0
			if err := sink.OnUtterance(Utterance{Text: te.Text, At: time.Now()}); err != nil {
				return false, err
			}
		case stt.KindTimeout:
			// Expected steady state during natural silence — not an error.
			*failures = 0
			if err := sink.OnTimeout(); err != nil {
				return false, err
			}
		case stt.KindFailure:
			eventErr := errors.New(te.ErrText)
			if failure, ok := event.(stt.Failure); ok && failure.Err != nil {
				eventErr = failure.Err
			}
			// Providers may enqueue context.Canceled just before their event
			// channel closes. Cancellation is the loop's clean-stop signal, not
			// a transcription failure to persist in the meeting record.
			if errors.Is(eventErr, context.Canceled) && ctx.Err() != nil {
				return false, nil
			}
			*failures++
			failed = true
			if err := sink.OnError(eventErr); err != nil {
				return false, err
			}
		}
	}
	return failed, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
