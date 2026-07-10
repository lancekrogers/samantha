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

// Utterance is one committed transcript segment from a continuous listening
// session, along with when it was captured.
type Utterance struct {
	Text string
	At   time.Time
}

// Sink receives events from a Loop. Implementations must not block for long —
// Loop calls these synchronously between STT events.
type Sink interface {
	OnUtterance(Utterance)
	OnTimeout()        // no speech heard within the STT provider's window
	OnError(err error) // a session failed; Loop is about to retry or give up
}

// Resetter clears buffered audio before a new session starts. *audio.Capture
// satisfies it; tests inject fakes.
type Resetter interface {
	Reset()
}

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
			sink.OnError(fmt.Errorf("start STT session: %w", err))
			if failures >= maxConsecutiveFailures {
				return fmt.Errorf("listen: %d consecutive STT failures, giving up: %w", failures, err)
			}
			if !sleepCtx(ctx, retryBackoff) {
				return nil
			}
			continue
		}

		failed := drainSession(session, sink, &failures)
		_ = session.Close()

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
func drainSession(session stt.Session, sink Sink, failures *int) (failed bool) {
	for event := range session.Events() {
		te := stt.ToTyped(event)
		switch te.Kind {
		case stt.KindFinalTranscript:
			*failures = 0
			sink.OnUtterance(Utterance{Text: te.Text, At: time.Now()})
		case stt.KindTimeout:
			// Expected steady state during natural silence — not an error.
			*failures = 0
			sink.OnTimeout()
		case stt.KindFailure:
			*failures++
			failed = true
			sink.OnError(errors.New(te.ErrText))
		}
	}
	return failed
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
