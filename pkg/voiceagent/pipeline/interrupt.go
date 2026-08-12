package pipeline

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	// bargeInArmDelay holds off interrupt detection after the turn's first
	// playback starts so the echo of Samantha's own first words doesn't trip
	// barge-in. Keep this short enough that "stop" / "wait" still interrupt
	// promptly once armed; AEC reference timing (not a long speech threshold)
	// is what suppresses self-echo.
	//
	// The delay applies once per turn (see applyPlaybackEvent), not once per
	// sentence: re-arming on every playback start left sentences shorter than
	// the window uninterruptible and kept barge-in permanently disarmed across
	// back-to-back short sentences (WI-dc9e33 B3 / F3.2).
	bargeInArmDelay = 600 * time.Millisecond
	// bargeInMinSpeechChunks requires sustained speech before interrupting, so a
	// brief burst of residual echo isn't mistaken for the user. One chunk is one
	// capture callback: Capture takes miniaudio's low-latency default period of
	// 10ms, so 6 chunks ≈ 60ms of consecutive speech energy, not the ~0.6s an
	// earlier comment here claimed. Short enough for "stop"/"no"; residual echo
	// is the AEC path's job, not this counter's.
	bargeInMinSpeechChunks = 6
	// bargeInBufferSize is the capture subscription depth for the controller.
	bargeInBufferSize = 8
	// bargeInPauseTailGuard skips capture chunks for a short window after
	// playback stops. Speaker echo keeps hitting the mic briefly after the
	// player drains between sentences; now that pauses stay armed, that tail
	// must not accumulate toward a trip. Chunks inside the guard are skipped,
	// not cleared — an utterance spanning the gap keeps its speech streak.
	bargeInPauseTailGuard = 200 * time.Millisecond
	// bargeInNearMissWindow bounds how recent below-threshold pause speech must
	// be for the turn to keep the capture buffer for the next listen (F3.3):
	// the user spoke during a pause but the turn completed before the trip
	// threshold, so their utterance is sitting in the capture ring buffer and
	// Reset() would delete it right before STT opens.
	bargeInNearMissWindow = 1500 * time.Millisecond
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
	tailGuard  time.Duration
	minSpeech  int
	bufferSize int

	// lastPauseSpeech is the UnixNano of the newest speech chunk heard while
	// armed but not playing — the near-miss evidence sawPauseSpeechWithin
	// reports after the watcher goroutine has been joined.
	lastPauseSpeech atomic.Int64
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
		tailGuard:  bargeInPauseTailGuard,
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
		var gapStart time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case samples, ok := <-chunks:
				if !ok {
					return
				}

				// Unarmed: the turn's first playback hasn't happened (armAt
				// zero — thinking-phase barge-in is a separate product call,
				// F3.4) or we're still inside the once-per-turn arm delay.
				// Reset so residual echo never accumulates toward a trip.
				at := armAt.Load()
				if at == 0 || time.Now().UnixNano() < at {
					consecutiveSpeech = 0
					c.vad.Clear()
					continue
				}

				// Armed for the rest of the turn. Playback pauses no longer
				// disarm (WI-dc9e33 B3): an utterance spanning a queue-drain
				// gap keeps its streak instead of restarting from zero. Only
				// the brief echo tail right after playback stops is skipped —
				// skipped, not cleared, so a spanning streak survives it.
				if c.playback.IsPlaying() {
					gapStart = time.Time{}
				} else {
					now := time.Now()
					if gapStart.IsZero() {
						gapStart = now
					}
					if now.Sub(gapStart) < c.tailGuard {
						continue
					}
				}

				c.vad.AcceptWaveform(samples)
				if c.vad.IsSpeech() {
					consecutiveSpeech++
					if !gapStart.IsZero() {
						c.lastPauseSpeech.Store(time.Now().UnixNano())
					}
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

// sawPauseSpeechWithin reports whether the watcher heard speech during a
// playback pause within the last window — the F3.3 near-miss signal: real user
// speech that never reached the trip threshold but is sitting in the capture
// ring buffer. Only meaningful after the watcher goroutine has been joined.
func (c *interruptController) sawPauseSpeechWithin(window time.Duration) bool {
	at := c.lastPauseSpeech.Load()
	if at == 0 {
		return false
	}
	return time.Since(time.Unix(0, at)) <= window
}
