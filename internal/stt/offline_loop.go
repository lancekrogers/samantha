package stt

import (
	"context"
	"errors"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// minSpeechSamples is the minimum number of audio samples for valid speech (0.3s at 16kHz).
const minSpeechSamples = 4800

// noFramePoll is how long the loop waits before re-polling a live source that
// has no audio buffered yet.
const noFramePoll = 10 * time.Millisecond

// samplesDuration returns the wall-time a chunk of n mono samples represents at
// the capture sample rate, derived from the sample count (no wall clock).
func samplesDuration(n int) time.Duration {
	if audio.SampleRate <= 0 || n <= 0 {
		return 0
	}
	return time.Duration(float64(n) / float64(audio.SampleRate) * float64(time.Second))
}

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
// Behavior mirrors the previous capture.Read() loop: listen until the VAD
// reports speech, transcribe finalized segments, reject speech shorter than
// minSpeechSamples, time out per the endpoint policy, and finalize explicitly on
// a finite source's Final frame instead of inferring EOF from empty reads.
func runOfflineLoop(ctx context.Context, deps offlineLoopDeps, events chan<- Event) {
	lastPhaseAt := time.Now()
	emitPhase := func(phase string) {
		now := time.Now()
		events <- PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}
		lastPhaseAt = now
	}

	deps.seg.Reset()
	emitPhase("listening")

	speechDetected := false
	start := time.Now()
	var speechSeen time.Duration
	var trailingSilence time.Duration

	// finalize collects buffered segments and transcribes them. It returns true
	// when the session is resolved (final transcript, timeout, or failure). When
	// final is false (mid-stream, too short, or empty), it returns to listening.
	finalize := func(final bool) bool {
		emitPhase("transcribing")

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
				events <- Failure{Err: err}
				return true
			}
			if text != "" {
				events <- FinalTranscript{Text: text}
				return true
			}
		}

		if final {
			events <- Timeout{}
			return true
		}
		speechDetected = false
		speechSeen = 0
		trailingSilence = 0
		start = time.Now()
		emitPhase("listening")
		return false
	}

	for {
		if err := ctx.Err(); err != nil {
			events <- Failure{Err: err}
			return
		}

		frame, err := deps.frames.ReadFrame(ctx)
		switch {
		case err == nil:
			// usable frame, possibly Final
		case errors.Is(err, audio.ErrNoFrameReady):
			if !speechDetected {
				d := deps.policy.Decide(endpoint.Observation{Elapsed: time.Since(start)})
				if d.Kind == endpoint.Timeout {
					events <- Timeout{}
					return
				}
			}
			time.Sleep(noFramePoll)
			continue
		case errors.Is(err, audio.ErrSourceClosed):
			frame = audio.Frame{Final: true}
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			events <- Failure{Err: err}
			return
		default:
			events <- Failure{Err: err}
			return
		}

		if len(frame.Samples) > 0 {
			frameDuration := samplesDuration(len(frame.Samples))
			deps.seg.AcceptWaveform(frame.Samples)
			if deps.seg.IsSpeech() {
				if !speechDetected {
					speechDetected = true
					emitPhase("hearing")
				}
				speechSeen += frameDuration
				trailingSilence = 0
			} else if speechDetected {
				trailingSilence += frameDuration
			}
		}

		if frame.Final {
			deps.seg.Flush()
			if !speechDetected && !deps.seg.HasSegments() {
				events <- Timeout{}
				return
			}
			finalize(true)
			return
		}

		decision := deps.policy.Decide(endpoint.Observation{
			HasSpeech:       speechDetected,
			SpeechSeen:      speechSeen,
			TrailingSilence: trailingSilence,
			Elapsed:         time.Since(start),
		})
		if decision.Kind == endpoint.TooShort {
			deps.seg.Reset()
			speechDetected = false
			speechSeen = 0
			trailingSilence = 0
			start = time.Now()
			emitPhase("listening")
			continue
		}
		if decision.Kind == endpoint.Finalize {
			deps.seg.Flush()
			finalize(true)
			return
		}

		if deps.seg.HasSegments() {
			if finalize(false) {
				return
			}
		}
	}
}
