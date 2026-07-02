package pipeline

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	// bargeInArmDelay holds off interrupt detection after playback starts so the
	// echo of Samantha's own first words doesn't trip barge-in.
	bargeInArmDelay = 600 * time.Millisecond
	// bargeInMinSpeechChunks requires sustained speech before interrupting, so a
	// brief burst of residual echo isn't mistaken for the user.
	bargeInMinSpeechChunks = 6
	// bargeInBufferSize is the capture subscription depth for the controller.
	bargeInBufferSize = 8
)

// interruptRequest is the typed barge-in signal the interrupt controller reports
// to the turn runtime, which owns the decision to act on it.
type interruptRequest struct {
	Reason string
	At     time.Time
}

// playbackState is the narrow view of playback the interrupt controller needs —
// only whether audio is currently playing — keeping it decoupled from playback
// internals.
type playbackState interface {
	IsPlaying() bool
}

// interruptController watches the capture stream for sustained user speech
// during playback and reports a single barge-in request. It owns its capture
// subscription and VAD lifecycle; its thresholds are explicit fields so tests
// can drive the arm delay and speech threshold deterministically.
type interruptController struct {
	capture    captureMonitor
	vad        voiceDetector
	playback   playbackState
	armDelay   time.Duration
	minSpeech  int
	bufferSize int
}

type interruptWatch struct {
	requests <-chan interruptRequest
	done     <-chan struct{}
}

// newInterruptController builds the controller from the pipeline's collaborators
// and the package barge-in tuning constants.
func (p *Pipeline) newInterruptController() *interruptController {
	return &interruptController{
		capture:    p.Capture,
		vad:        p.BargeInVAD,
		playback:   p.Player,
		armDelay:   bargeInArmDelay,
		minSpeech:  bargeInMinSpeechChunks,
		bufferSize: bargeInBufferSize,
	}
}

// enabled reports whether every collaborator required for barge-in is present.
func (c *interruptController) enabled() bool {
	return c.capture != nil && c.vad != nil && c.playback != nil
}

// watch subscribes to capture and reports one barge-in request once sustained
// speech is detected, but only after playback is active and the arm window
// (tracked via armAt) has elapsed. The returned channel is nil when barge-in is
// disabled. The goroutine always unsubscribes from capture and clears the VAD on
// exit, and stops promptly on ctx cancellation.
func (c *interruptController) watch(ctx context.Context, armAt *atomic.Int64) <-chan interruptRequest {
	return c.watchWithDone(ctx, armAt).requests
}

func (c *interruptController) watchWithDone(ctx context.Context, armAt *atomic.Int64) interruptWatch {
	if !c.enabled() {
		return interruptWatch{}
	}

	out := make(chan interruptRequest, 1)
	done := make(chan struct{})
	subscriptionID, chunks := c.capture.Subscribe(c.bufferSize)

	go func() {
		defer close(done)
		defer c.capture.Unsubscribe(subscriptionID)
		defer c.vad.Clear()

		consecutiveSpeech := 0
		for {
			select {
			case <-ctx.Done():
				return
			case samples, ok := <-chunks:
				if !ok {
					return
				}

				// Disarmed: no playback, or still inside the post-start arm
				// delay. Reset so residual echo never accumulates toward a trip.
				if !c.playback.IsPlaying() || time.Now().UnixNano() < armAt.Load() {
					consecutiveSpeech = 0
					c.vad.Clear()
					continue
				}

				c.vad.AcceptWaveform(samples)
				if c.vad.IsSpeech() {
					consecutiveSpeech++
				} else {
					consecutiveSpeech = 0
				}

				if c.vad.IsSpeechDetected() || consecutiveSpeech >= c.minSpeech {
					select {
					case out <- interruptRequest{Reason: "barge_in", At: time.Now()}:
					default:
					}
					return
				}
			}
		}
	}()

	return interruptWatch{requests: out, done: done}
}
