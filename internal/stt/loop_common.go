package stt

import (
	"context"
	"errors"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// sendEvent delivers an event, preferring a non-blocking send and falling back
// to a cancellable blocking send when the channel is full.
func sendEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	default:
	}
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

// newPhaseEmitter returns a phase-event emitter that stamps the elapsed time
// between phases. It returns false when the context is cancelled mid-send, so
// loops can stop promptly.
func newPhaseEmitter(ctx context.Context, events chan<- Event) func(string) bool {
	lastPhaseAt := time.Now()
	return func(phase string) bool {
		now := time.Now()
		if !sendEvent(ctx, events, PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}) {
			return false
		}
		lastPhaseAt = now
		return true
	}
}

// frameRead is the triaged result of one FrameSource read, shared by the
// provider loops so both classify source behavior identically.
type frameRead struct {
	frame audio.Frame
	ready bool  // a frame (possibly Final) is available
	eof   bool  // the source has ended
	err   error // fatal: the loop should emit Failure and return
}

// readLoopFrame reads one frame and classifies the result. ErrNoFrameReady is
// not an error: it yields an empty read so the loop can still evaluate the
// endpoint policy before polling again.
func readLoopFrame(ctx context.Context, frames audio.FrameSource) frameRead {
	frame, err := frames.ReadFrame(ctx)
	switch {
	case err == nil:
		return frameRead{frame: frame, ready: true, eof: frame.Final}
	case errors.Is(err, audio.ErrNoFrameReady):
		return frameRead{}
	case errors.Is(err, audio.ErrSourceClosed):
		return frameRead{frame: audio.Frame{Final: true}, ready: true, eof: true}
	default:
		return frameRead{err: err}
	}
}

// frameDur returns the wall-time a frame represents, preferring the source's
// own stamp and falling back to the sample count for sources that omit it.
func frameDur(f audio.Frame) time.Duration {
	if f.Duration > 0 {
		return f.Duration
	}
	return audio.SamplesDuration(len(f.Samples))
}

// speechTracker accumulates the speech facts the endpoint policy evaluates.
// It anchors the utterance clock at speech onset so pre-speech silence never
// deducts from the allowed utterance length.
type speechTracker struct {
	listenStart time.Time
	speechStart time.Time
	detected    bool
	seen        time.Duration
	silence     time.Duration
}

func newSpeechTracker() *speechTracker {
	return &speechTracker{listenStart: time.Now()}
}

// observe folds one frame's VAD verdict into the tracker, reporting speech onset.
func (t *speechTracker) observe(isSpeech bool, dur time.Duration) (onset bool) {
	if isSpeech {
		onset = t.markSpeech()
		t.seen += dur
		t.silence = 0
		return onset
	}
	if t.detected {
		t.silence += dur
	}
	return false
}

// markSpeech records speech onset from a non-VAD signal (a recognizer partial,
// an EOF flush), reporting whether this call was the onset.
func (t *speechTracker) markSpeech() bool {
	if t.detected {
		return false
	}
	t.detected = true
	t.speechStart = time.Now()
	return true
}

// observation snapshots the tracker for the policy. It is evaluated once per
// loop iteration — including no-frame ticks — so every endpoint decision is
// enforced even when the source stops delivering frames mid-utterance.
func (t *speechTracker) observation(providerEnd, eof bool) endpoint.Observation {
	o := endpoint.Observation{
		HasSpeech:       t.detected,
		SpeechSeen:      t.seen,
		TrailingSilence: t.silence,
		Elapsed:         time.Since(t.listenStart),
		ProviderEnd:     providerEnd,
		SourceFinal:     eof,
	}
	if t.detected {
		o.SpeechElapsed = time.Since(t.speechStart)
	}
	return o
}

// reset returns the tracker to a fresh listening window.
func (t *speechTracker) reset() {
	t.detected = false
	t.seen = 0
	t.silence = 0
	t.listenStart = time.Now()
	t.speechStart = time.Time{}
}
