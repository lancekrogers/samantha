package audio

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// Echo cancellation is easy to wire up and have do nothing. The reference path
// can be rate-correct, phase-correct and still deliver ~0dB because the far-end
// audio reaches the canceller tens of milliseconds before its echo reaches the
// mic — outside the filter's tap window. These tests drive the front-end the way
// the devices do and assert on measured ERLE, so a regression shows up as a
// number rather than as "barge-in feels twitchy again".

const (
	// erleCaptureChunk is one capture callback: miniaudio's low-latency default
	// period is 10ms, and Capture does not override it.
	erleCaptureChunk = SampleRate / 100
	// erleEchoGain is how loud the speaker is at the mic.
	erleEchoGain = 0.5
)

// farEndSpeech generates a speech-like far-end signal: band-limited noise under
// a syllable-rate envelope. White noise would flatter the canceller.
func farEndSpeech(n int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	var lp float64
	for i := range out {
		lp = 0.85*lp + 0.15*rng.NormFloat64()
		env := 0.5 + 0.5*math.Sin(2*math.Pi*float64(i)/float64(SampleRate)*3)
		out[i] = float32(lp * env * 0.3)
	}
	return out
}

// erleThroughFrontend runs the real push/pop cadence: each iteration pushes one
// callback of far-end audio and processes one callback of mic audio whose echo
// lags the push by echoLag samples. It reports dB of echo suppression as seen
// by VAD — measured against the same chain with no reference fed, so the NS and
// AGC stages cannot take credit for the AEC's work.
func erleThroughFrontend(t *testing.T, refDelay, echoLag int) float64 {
	t.Helper()

	const seconds = 8
	total := SampleRate * seconds
	far := farEndSpeech(total+echoLag, 1)

	run := func(feedRef bool) float64 {
		f := NewVoiceFrontend()
		f.SetReferenceDelay(refDelay)

		var energy float64
		for start := 0; start+erleCaptureChunk <= total; start += erleCaptureChunk {
			if feedRef {
				f.PushPlaybackReference(far[start : start+erleCaptureChunk])
			}
			mic := make([]float32, erleCaptureChunk)
			for i := range mic {
				// The mic hears what left the speaker echoLag samples ago.
				if src := start + i - echoLag; src >= 0 {
					mic[i] = erleEchoGain * far[src]
				}
			}
			out := f.ProcessCapture(mic)
			// Score the back half only, after the filter has adapted.
			if start > total/2 {
				for _, s := range out {
					energy += float64(s) * float64(s)
				}
			}
		}
		return energy
	}

	withRef := run(true)
	withoutRef := run(false)
	return 10 * math.Log10(withoutRef/math.Max(withRef, 1e-12))
}

// The shipping configuration must cancel at the latency the player actually
// creates, on every rate it will actually open. playbackRateCandidates tries
// the TTS rate first, so 24kHz is the common case, not 48kHz — and the same
// 3x512-frame ring is 64ms of output latency there against 32ms at 48kHz,
// while referenceDelaySamples under-counts by design. A suite that only
// covered 48kHz stayed green while the preferred path cancelled ~1dB.
func TestEchoCancellerERLEAcrossDeviceLatency(t *testing.T) {
	ms := func(d int) int { return d * 1000 / SampleRate }
	// Thresholds sit a couple of dB under measured so platform float
	// differences do not flake, while a real regression — a dropped delay
	// offset, a shortened filter, a slower step — falls well through:
	// uncompensated, every one of these reads under 2.5dB.
	extras := []struct {
		name    string
		extraMs int
		minERLE float64
	}{
		{"ring buffer only", 0, 6.5},
		{"ring + driver latency", 8, 6},
		{"ring + driver + far speaker", 16, 5.5},
	}

	for _, rate := range []int{24000, 44100, 48000} {
		t.Run(fmt.Sprintf("%dHz", rate), func(t *testing.T) {
			refDelay := referenceDelaySamples(rate, playbackPeriodFrames)
			// The whole ring must drain before the first frame is audible.
			ring := playbackPeriods * playbackPeriodFrames * SampleRate / rate

			for _, tc := range extras {
				t.Run(tc.name, func(t *testing.T) {
					echoLag := ring + tc.extraMs*SampleRate/1000
					erle := erleThroughFrontend(t, refDelay, echoLag)
					t.Logf("rate %dHz: echo lag %dms, reference delay %dms, residual %dms, "+
						"%d taps (%dms) → ERLE %+.2f dB",
						rate, ms(echoLag), ms(refDelay), ms(echoLag-refDelay),
						echoCancellerTaps, ms(echoCancellerTaps), erle)
					if erle < tc.minERLE {
						t.Errorf("ERLE = %+.2f dB, want >= %+.2f dB — self-echo at this "+
							"latency will reach VAD and trip barge-in", erle, tc.minERLE)
					}
				})
			}
		})
	}
}

// The residual the tap window has to span is (true output latency - estimated
// delay), and the estimate is deliberately low. Lock the relationship the ERLE
// numbers depend on so a change to periods, taps, or the estimate cannot
// quietly move the worst rate past the cliff.
func TestEchoCancellerTapsCoverWorstRateResidual(t *testing.T) {
	// Driver and acoustic latency beyond the ring, in ms. The ERLE cases above
	// assert cancellation holds this far out.
	const extraMs = 16

	for _, rate := range []int{24000, 44100, 48000} {
		ring := playbackPeriods * playbackPeriodFrames * SampleRate / rate
		residual := ring + extraMs*SampleRate/1000 - referenceDelaySamples(rate, playbackPeriodFrames)
		if residual > echoCancellerTaps {
			t.Errorf("rate %dHz: residual echo %dms exceeds the %dms tap window; "+
				"cancellation falls off a cliff past the last tap",
				rate, residual*1000/SampleRate, echoCancellerTaps*1000/SampleRate)
		}
	}
}

// The regression itself: with no reference delay the canceller is handed
// far-end audio tens of milliseconds before its echo arrives, which no amount
// of rate alignment fixes. This is what shipped before SetReferenceDelay
// existed.
//
// Measured at 24kHz — the rate playbackRateCandidates prefers — because the
// tap window alone now covers the 48kHz profile: 768 taps span 48ms, more than
// that ring's 32ms + driver lag, so an uncompensated 48kHz run scores ~7dB and
// proves nothing. At 24kHz the ring is 64ms and the offset is still the only
// thing that brings the echo inside the window.
func TestEchoCancellerNeedsReferenceDelay(t *testing.T) {
	const rate = 24000
	echoLag := playbackPeriods*playbackPeriodFrames*SampleRate/rate + 8*SampleRate/1000

	compensated := erleThroughFrontend(t, referenceDelaySamples(rate, playbackPeriodFrames), echoLag)
	uncompensated := erleThroughFrontend(t, 0, echoLag)
	t.Logf("compensated %+.2f dB vs uncompensated %+.2f dB", compensated, uncompensated)

	if uncompensated > 3 {
		t.Fatalf("uncompensated ERLE = %+.2f dB; the test no longer reproduces the "+
			"delay bug it guards", uncompensated)
	}
	if compensated < uncompensated+4 {
		t.Errorf("reference delay bought only %+.2f dB (%.2f → %.2f); the offset is "+
			"not doing its job", compensated-uncompensated, uncompensated, compensated)
	}
}

// Barge-in only works if the user survives the canceller. A longer filter and
// a faster step both raise the risk that the AEC adapts to near-end speech and
// subtracts it away, so hold that line explicitly: when the user talks over
// playback, their voice must still dominate the front-end's output.
func TestEchoCancellerPreservesNearEndDuringDoubleTalk(t *testing.T) {
	const (
		echoLag = 40 * SampleRate / 1000
		total   = SampleRate * 8
	)
	refDelay := referenceDelaySamples(48000, playbackPeriodFrames)
	far := farEndSpeech(total+echoLag, 1)
	near := farEndSpeech(total, 99) // the user, uncorrelated with playback

	f := NewVoiceFrontend()
	f.SetReferenceDelay(refDelay)

	var outEnergy, nearEnergy float64
	for start := 0; start+erleCaptureChunk <= total; start += erleCaptureChunk {
		f.PushPlaybackReference(far[start : start+erleCaptureChunk])
		mic := make([]float32, erleCaptureChunk)
		for i := range mic {
			if src := start + i - echoLag; src >= 0 {
				mic[i] = erleEchoGain * far[src]
			}
			// The user starts talking halfway in — the barge-in moment.
			if start > total/2 {
				mic[i] += near[start+i]
			}
		}
		out := f.ProcessCapture(mic)
		if start > total*3/4 { // score once double-talk is well established
			for i, s := range out {
				outEnergy += float64(s) * float64(s)
				nearEnergy += float64(near[start+i]) * float64(near[start+i])
			}
		}
	}

	kept := 10 * math.Log10(outEnergy/math.Max(nearEnergy, 1e-12))
	t.Logf("near-end preserved through double-talk: %+.2f dB", kept)
	if kept < 0 {
		t.Errorf("near-end preservation = %+.2f dB; the canceller is eating the "+
			"user's voice, which is exactly what barge-in needs to hear", kept)
	}
}

// A burst that starts after silence must re-prime the offset: the queue drains
// to empty between utterances, and without re-priming every sentence after the
// first would be uncompensated.
func TestReferenceDelayRePrimesAfterSilence(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(320)

	push := make([]float32, 160)
	for i := range push {
		push[i] = 0.5
	}

	// First burst: the offset is primed, so the first pops are the silent
	// device-buffer window rather than far-end audio.
	f.PushPlaybackReference(push)
	if out := f.refsSnapshot(); len(out) != 320+160 {
		t.Fatalf("queue depth after first push = %d, want 320 delay + 160 pushed", len(out))
	}

	// Drain it the way continuous capture does during silence.
	for range 10 {
		f.ProcessCapture(make([]float32, 160))
	}
	if got := len(f.refsSnapshot()); got != 0 {
		t.Fatalf("queue depth after silence = %d, want 0 (drained)", got)
	}

	// Next burst re-primes rather than pairing immediately.
	f.PushPlaybackReference(push)
	if out := f.refsSnapshot(); len(out) != 320+160 {
		t.Fatalf("queue depth after second burst = %d, want the offset re-primed", len(out))
	}
}
