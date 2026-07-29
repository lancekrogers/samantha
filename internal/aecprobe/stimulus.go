// Package aecprobe measures the voice front-end's echo cancellation against a
// real speaker and a real microphone.
//
// The unit tests in internal/audio score the canceller against a synthetic
// echo: one delayed, scaled copy of the far-end, perfectly linear, in a silent
// room. That is enough to catch an unaligned reference, and it is what caught
// the 24kHz tap-budget regression, but it cannot answer whether the filter
// works. Real echo is a reverberant impulse response, real speakers are
// nonlinear, and the playback and capture clocks drift apart. This package
// plays a known signal through the actual player, records what the microphone
// actually hears, and scores the result.
package aecprobe

import "math"

// Rate is the capture clock. Everything scored here is resampled to it, because
// that is the domain the canceller works in.
const Rate = 16000

// Chirp sweeps a frequency range so its autocorrelation has one sharp peak,
// which is what makes it usable for delay estimation. A speech-shaped signal
// correlates broadly with itself and would place the peak ambiguously.
//
// The sweep stays inside the capture band: content above Rate/2 cannot survive
// the microphone's own anti-aliasing and would only add a noise floor to the
// correlation.
func Chirp(seconds float64, rate int) []float32 {
	if seconds <= 0 || rate <= 0 {
		return nil
	}
	n := int(seconds * float64(rate))
	out := make([]float32, n)

	lo, hi := 200.0, math.Min(float64(Rate)/2*0.9, float64(rate)/2*0.9)
	// Exponential sweep: constant energy per octave, so the low end is not
	// under-represented the way a linear sweep leaves it.
	k := math.Log(hi/lo) / seconds

	for i := range out {
		t := float64(i) / float64(rate)
		phase := 2 * math.Pi * lo * (math.Exp(k*t) - 1) / k
		// Taper the ends: a hard start or stop is a broadband click that shows
		// up in the correlation as a second, spurious peak.
		out[i] = float32(0.5 * math.Sin(phase) * taper(i, n, rate/50))
	}
	return out
}

// SpeechLikeNoise is the ERLE stimulus: band-limited noise under a
// syllable-rate envelope. It is deterministic given seed so two runs on
// different days are comparable.
//
// Real TTS output would be more faithful still, and the operator procedure
// covers that as a second pass — but it varies per voice and per provider,
// which makes cross-device comparison harder. This is the controlled baseline.
func SpeechLikeNoise(seconds float64, rate int, seed int64) []float32 {
	if seconds <= 0 || rate <= 0 {
		return nil
	}
	n := int(seconds * float64(rate))
	out := make([]float32, n)

	rng := newDeterministicRNG(seed)
	var lp float64
	for i := range out {
		lp = 0.85*lp + 0.15*rng.normal()
		env := 0.5 + 0.5*math.Sin(2*math.Pi*float64(i)/float64(rate)*3)
		out[i] = float32(lp * env * 0.3 * taper(i, n, rate/50))
	}
	return out
}

// Silence is used between phases so the reference queue drains and each phase
// starts from the same state — the burst re-prime path the canceller relies on.
func Silence(seconds float64, rate int) []float32 {
	if seconds <= 0 || rate <= 0 {
		return nil
	}
	return make([]float32, int(seconds*float64(rate)))
}

// taper returns a 0..1 raised-cosine window value, ramping over edge samples at
// each end and flat in between.
func taper(i, n, edge int) float64 {
	if edge <= 0 || n <= 2*edge {
		return 1
	}
	switch {
	case i < edge:
		return 0.5 * (1 - math.Cos(math.Pi*float64(i)/float64(edge)))
	case i >= n-edge:
		return 0.5 * (1 - math.Cos(math.Pi*float64(n-1-i)/float64(edge)))
	default:
		return 1
	}
}

// deterministicRNG is a small xorshift plus Box-Muller. math/rand would work,
// but pinning the generator here means a metrics.json from today stays
// comparable to one from a future Go release.
type deterministicRNG struct {
	state    uint64
	spare    float64
	hasSpare bool
}

func newDeterministicRNG(seed int64) *deterministicRNG {
	s := uint64(seed)
	if s == 0 {
		s = 0x9E3779B97F4A7C15
	}
	return &deterministicRNG{state: s}
}

func (r *deterministicRNG) uniform() float64 {
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	return float64(r.state>>11) / float64(1<<53)
}

func (r *deterministicRNG) normal() float64 {
	if r.hasSpare {
		r.hasSpare = false
		return r.spare
	}
	u := math.Max(r.uniform(), 1e-12)
	v := r.uniform()
	mag := math.Sqrt(-2 * math.Log(u))
	r.spare = mag * math.Sin(2*math.Pi*v)
	r.hasSpare = true
	return mag * math.Cos(2*math.Pi*v)
}
