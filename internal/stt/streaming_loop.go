package stt

import (
	"context"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// streamingRecognizer abstracts the cgo online recognizer so the streaming loop
// can be tested with a deterministic fake. The production implementation is
// onlineRec, wrapping sherpa-onnx.
type streamingRecognizer interface {
	Accept(samples []float32) // feed audio and run any ready decode steps
	Partial() string          // current partial transcript
	IsEndpoint() bool         // recognizer-signalled endpoint
	Finalize() string         // flush remaining audio and return the final transcript
	Reset() error             // start a fresh utterance after a false-positive finalize
}

// streamingLoopDeps are the injected seams the streaming STT loop drives.
type streamingLoopDeps struct {
	frames audio.FrameSource
	seg    segmenter
	rec    streamingRecognizer
	policy endpoint.Policy
}

// runStreamingLoop drives streaming STT over the typed frame contract. It emits
// partial transcripts as they arrive and finalizes when the source ends, the
// VAD finalizes a segment, or the endpoint policy decides to. The recognizer's
// endpoint fact is fed into the shared policy (which runs with AllowProviderEnd
// set). It is free of cgo so it can be tested with fakes; it does not close
// events.
//
// Like the offline loop, the endpoint policy is evaluated exactly once per
// iteration — including no-frame ticks — and every DecisionKind is handled, so
// a false-positive speech mark (a spurious partial with no real speech) is
// reset by TooShort instead of suppressing the timeouts forever.
func runStreamingLoop(ctx context.Context, deps streamingLoopDeps, events chan<- Event) {
	emitPhase := newPhaseEmitter(ctx, events)

	deps.seg.Reset()
	if !emitPhase("listening") {
		return
	}

	track := newSpeechTracker()
	transcribing := false
	lastPartial := ""

	// resetListening discards a rejected or false-positive utterance and opens a
	// fresh listening window. It returns false when the loop should stop.
	resetListening := func() bool {
		deps.seg.Reset()
		if err := deps.rec.Reset(); err != nil {
			sendEvent(ctx, events, Failure{Err: err})
			return false
		}
		track.reset()
		transcribing = false
		lastPartial = ""
		return emitPhase("listening")
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
		eof := read.eof

		if read.ready && len(read.frame.Samples) > 0 {
			deps.seg.AcceptWaveform(read.frame.Samples)
			if track.observe(deps.seg.IsSpeech(), frameDur(read.frame)) {
				if !emitPhase("hearing") {
					return
				}
			}

			deps.rec.Accept(read.frame.Samples)
			partial := normalizeTranscript(strings.TrimSpace(deps.rec.Partial()))
			if partial != "" {
				if track.markSpeech() {
					if !emitPhase("hearing") {
						return
					}
				}
				if !transcribing {
					transcribing = true
					if !emitPhase("transcribing") {
						return
					}
				}
				if partial != lastPartial {
					if !sendEvent(ctx, events, PartialTranscript{Text: partial}) {
						return
					}
					lastPartial = partial
				}
			}
		}

		if eof {
			deps.seg.Flush()
			if deps.seg.HasSegments() {
				track.markSpeech()
			}
		}

		// The recognizer's endpoint fact only matters once speech was marked
		// (the policy ignores it before then), so skip the cgo call while idle.
		providerEnd := track.detected && deps.rec.IsEndpoint()
		decision := deps.policy.Decide(track.observation(providerEnd, eof))

		switch decision.Kind {
		case endpoint.Timeout, endpoint.SourceExhausted:
			sendEvent(ctx, events, Timeout{})
			return
		case endpoint.TooShort:
			// A live rejection (spurious partial, sub-MinSpeech blip) resets to
			// a fresh window; at EOF fall through so the recognizer still gets
			// its Finalize chance on whatever was buffered.
			if !eof {
				if !resetListening() {
					return
				}
				continue
			}
		}

		// The provider-end fact reaches the decision through the policy's
		// AllowProviderEnd gate (decision.Kind == Finalize); it is deliberately
		// not consulted raw here, so an ungated recognizer endpoint cannot
		// finalize on its own.
		shouldFinalize := track.detected && (eof || deps.seg.HasSegments() || decision.Kind == endpoint.Finalize)
		if !shouldFinalize {
			if !read.ready {
				time.Sleep(noFramePoll)
			}
			continue
		}

		if !transcribing {
			transcribing = true
			if !emitPhase("transcribing") {
				return
			}
		}

		finalText := normalizeTranscript(strings.TrimSpace(deps.rec.Finalize()))
		if finalText != "" {
			sendEvent(ctx, events, FinalTranscript{Text: finalText})
			return
		}
		if eof {
			sendEvent(ctx, events, Timeout{})
			return
		}

		// False positive or empty decode: reset and keep listening.
		if !resetListening() {
			return
		}
	}
}
