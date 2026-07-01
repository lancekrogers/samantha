package stt

import (
	"context"
	"errors"
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
// recognizer signals an endpoint, the VAD finalizes a segment, or the endpoint
// policy decides to. The recognizer's endpoint fact is fed into the shared
// policy (which runs with AllowProviderEnd set). It is free of cgo so it can be
// tested with fakes; it does not close events.
func runStreamingLoop(ctx context.Context, deps streamingLoopDeps, events chan<- Event) {
	lastPhaseAt := time.Now()
	emitPhase := func(phase string) bool {
		now := time.Now()
		if !sendEvent(ctx, events, PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}) {
			return false
		}
		lastPhaseAt = now
		return true
	}

	deps.seg.Reset()
	if !emitPhase("listening") {
		return
	}

	speechDetected := false
	transcribing := false
	start := time.Now()
	var speechSeen time.Duration
	var trailingSilence time.Duration
	lastPartial := ""

	markSpeech := func() {
		if !speechDetected {
			speechDetected = true
			emitPhase("hearing")
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			sendEvent(ctx, events, Failure{Err: err})
			return
		}

		if !speechDetected {
			if deps.policy.Decide(endpoint.Observation{Elapsed: time.Since(start)}).Kind == endpoint.Timeout {
				sendEvent(ctx, events, Timeout{})
				return
			}
		}

		frame, err := deps.frames.ReadFrame(ctx)
		eof := false
		switch {
		case err == nil:
			eof = frame.Final
		case errors.Is(err, audio.ErrNoFrameReady):
			time.Sleep(noFramePoll)
			continue
		case errors.Is(err, audio.ErrSourceClosed):
			eof = true
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			sendEvent(ctx, events, Failure{Err: err})
			return
		default:
			sendEvent(ctx, events, Failure{Err: err})
			return
		}

		if len(frame.Samples) > 0 {
			frameDuration := samplesDuration(len(frame.Samples))
			deps.seg.AcceptWaveform(frame.Samples)
			if deps.seg.IsSpeech() {
				markSpeech()
				speechSeen += frameDuration
				trailingSilence = 0
			} else if speechDetected {
				trailingSilence += frameDuration
			}

			deps.rec.Accept(frame.Samples)
			partial := normalizeTranscript(strings.TrimSpace(deps.rec.Partial()))
			if partial != "" {
				markSpeech()
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
				markSpeech()
			}
		}

		decision := deps.policy.Decide(endpoint.Observation{
			HasSpeech:       speechDetected,
			SpeechSeen:      speechSeen,
			TrailingSilence: trailingSilence,
			Elapsed:         time.Since(start),
			ProviderEnd:     deps.rec.IsEndpoint(),
			SourceFinal:     eof,
		})

		switch decision.Kind {
		case endpoint.Timeout, endpoint.SourceExhausted:
			sendEvent(ctx, events, Timeout{})
			return
		}

		// The provider-end fact reaches the decision through the policy's
		// AllowProviderEnd gate (decision.Kind == Finalize); it is deliberately
		// not consulted raw here, so an ungated recognizer endpoint cannot
		// finalize on its own.
		shouldFinalize := speechDetected && (eof || deps.seg.HasSegments() || decision.Kind == endpoint.Finalize)
		if !shouldFinalize {
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
		deps.seg.Reset()
		if err := deps.rec.Reset(); err != nil {
			sendEvent(ctx, events, Failure{Err: err})
			return
		}
		speechDetected = false
		transcribing = false
		speechSeen = 0
		trailingSilence = 0
		lastPartial = ""
		start = time.Now()
		if !emitPhase("listening") {
			return
		}
	}
}
