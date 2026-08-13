package aecprobe

import (
	"math"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
)

// MeasureDelay finds the lag, in samples, that best aligns want inside got.
//
// This is the number the whole exercise exists to produce. referenceDelaySamples
// infers output latency from (Periods-1)*PeriodSizeInFrames and an assumed
// period count; measuring it against a real speaker and microphone is what
// showed it under-counting by 60ms on built-in hardware.
//
// Delegates to audio.CorrelateDelay so the probe and the runtime calibrator
// cannot drift apart on what "the delay" means.
//
// Returns the lag and a confidence in [0,1] — the peak's normalized
// correlation. A low confidence means the peak is not trustworthy: too quiet,
// too noisy, or the stimulus never actually played.
func MeasureDelay(want, got []float32, maxLagSamples int) (lag int, confidence float64) {
	// Correlate over up to a second of the stimulus: the sweep's
	// autocorrelation peak is sharp, and a bounded window keeps this
	// O(maxLag*window) rather than O(maxLag*len).
	return audio.CorrelateDelay(want, got, maxLagSamples, Rate)
}

// ERLE reports echo return loss enhancement in dB: how much quieter the
// front-end's output is than its input over the same span.
//
// Positive is cancellation. This is measured on real recordings, so unlike the
// unit tests it cannot run the same audio twice with the reference withheld —
// the comparison is mic-in against mic-out. That means the noise suppressor and
// AGC are inside the number, which is the honest framing anyway: VAD sees the
// end of the chain, not the canceller's output.
func ERLE(micIn, micOut []float32) float64 {
	in, out := energy(micIn), energy(micOut)
	if in <= 0 {
		return 0
	}
	return 10 * math.Log10(in/math.Max(out, 1e-12))
}

// SegmentRMS reports the RMS of a span, used to check that playback was
// actually audible. An ERLE of +40dB means nothing if the speaker was muted.
func SegmentRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	return math.Sqrt(energy(samples) / float64(len(samples)))
}

// PeakLevel reports the largest absolute sample, so a run that clipped is
// visible in metrics.json. Clipping makes the echo path nonlinear, which no
// linear canceller can model — a clipped run's ERLE is not a filter verdict.
func PeakLevel(samples []float32) float64 {
	peak := 0.0
	for _, s := range samples {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}
	return peak
}

// Slice returns samples[from:to] clamped to what exists, so phase marks taken
// from a live recording can never panic a run at the reporting stage.
func Slice(samples []float32, from, to int) []float32 {
	if from < 0 {
		from = 0
	}
	if to > len(samples) {
		to = len(samples)
	}
	if from >= to {
		return nil
	}
	return samples[from:to]
}

func energy(samples []float32) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return sum
}
