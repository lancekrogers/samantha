package stt

import (
	"context"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// minSpeechSamples is the minimum number of audio samples for valid speech (0.3s at 16kHz).
const minSpeechSamples = 4800

// noFramePoll is how long the loop waits before re-polling a live source that
// has no audio buffered yet.
const noFramePoll = 10 * time.Millisecond

// segmenter abstracts the voice-activity detector the offline STT loop drives,
// so the loop can be exercised with a deterministic fake instead of the cgo
// Silero VAD. The production implementation is vadSegmenter.
type segmenter interface {
	AcceptWaveform(samples []float32) // feed a chunk of captured audio
	IsSpeech() bool                   // the current chunk contains speech
	HasSegments() bool                // one or more finalized segments are available
	NextSegment() ([]float32, bool)   // pop the next finalized segment
	Reset()                           // discard pending state between turns
	Flush()                           // finalize trailing audio at end of stream
}

// transcribeFunc decodes finalized speech samples into text. The production
// implementation wraps the cgo sherpa recognizer; tests inject a fake.
type transcribeFunc func(samples []float32) (string, error)

// offlineLoopDeps are the injected seams the offline STT loop drives. None of
// them require cgo or model files at construction, so the loop is fully testable.
type offlineLoopDeps struct {
	frames     audio.FrameSource
	seg        segmenter
	policy     endpoint.Policy
	transcribe transcribeFunc
}

// runOfflineLoop drives utterance-final STT over the typed frame contract and
// the endpoint policy. It is free of cgo and model dependencies so it can be
// tested with fakes. It does not close events; the caller owns that lifecycle.
//
// The endpoint policy is evaluated exactly once per iteration — including
// iterations where the source has no frame ready — so the start timeout and
// the utterance cap are enforced even when a live source stalls mid-utterance.
func runOfflineLoop(ctx context.Context, deps offlineLoopDeps, events chan<- Event) {
	emitPhase := newPhaseEmitter(ctx, events)

	deps.seg.Reset()
	if !emitPhase("listening") {
		return
	}

	track := newSpeechTracker()
	var levels levelEmitter

	// finalize collects buffered segments and transcribes them. It returns true
	// when the session is resolved (final transcript, timeout, or failure). When
	// final is false (mid-stream, too short, or empty), it returns to listening.
	finalize := func(final bool) bool {
		if !emitPhase("transcribing") {
			return true
		}

		var samples []float32
		for {
			seg, ok := deps.seg.NextSegment()
			if !ok {
				break
			}
			samples = append(samples, seg...)
		}

		if len(samples) >= minSpeechSamples {
			text, err := deps.transcribe(samples)
			if err != nil {
				sendEvent(ctx, events, Failure{Err: err})
				return true
			}
			if text != "" {
				sendEvent(ctx, events, FinalTranscript{Text: text})
				return true
			}
		}

		if final {
			sendEvent(ctx, events, Timeout{})
			return true
		}
		track.reset()
		return !emitPhase("listening")
	}

	for {
		if err := ctx.Err(); err != nil {
			sendEvent(ctx, events, Failure{Err: err})
			return
		}

		read := readLoopFrame(ctx, deps.frames)
		if read.err != nil {
			sendEvent(ctx, events, Failure{Err: read.err})
			return
		}

		if read.ready && len(read.frame.Samples) > 0 {
			levels.maybeEmit(events, read.frame.Samples)
			deps.seg.AcceptWaveform(read.frame.Samples)
			if track.observe(deps.seg.IsSpeech(), frameDur(read.frame)) {
				if !emitPhase("hearing") {
					return
				}
			}
		}

		if read.eof {
			deps.seg.Flush()
			if !track.detected && !deps.seg.HasSegments() {
				sendEvent(ctx, events, Timeout{})
				return
			}
			finalize(true)
			return
		}

		decision := deps.policy.Decide(track.observation(false, false))
		switch decision.Kind {
		case endpoint.Timeout:
			sendEvent(ctx, events, Timeout{})
			return
		case endpoint.TooShort:
			deps.seg.Reset()
			track.reset()
			if !emitPhase("listening") {
				return
			}
			continue
		case endpoint.Finalize:
			deps.seg.Flush()
			finalize(true)
			return
		}

		if deps.seg.HasSegments() {
			if finalize(false) {
				return
			}
			continue
		}

		if !read.ready {
			time.Sleep(noFramePoll)
		}
	}
}
