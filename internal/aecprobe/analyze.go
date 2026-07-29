package aecprobe

import "math"

// MeasureDelay finds the lag, in samples, that best aligns want inside got.
//
// This is the number the whole exercise exists to produce. referenceDelaySamples
// infers output latency from (Periods-1)*PeriodSizeInFrames and an assumed
// period count; nothing has ever checked it against a speaker and a microphone.
// Cross-correlating the played chirp against the recording answers it directly.
//
// Returns the lag and a confidence in [0,1] — the peak's normalized correlation.
// A low confidence means the peak is not trustworthy: too quiet, too noisy, or
// the chirp never actually played.
func MeasureDelay(want, got []float32, maxLagSamples int) (lag int, confidence float64) {
	if len(want) == 0 || len(got) == 0 || maxLagSamples <= 0 {
		return 0, 0
	}
	// Correlate over a window of the stimulus, not all of it: the sweep's
	// autocorrelation peak is sharp, and a shorter window keeps this O(lag*win)
	// instead of O(lag*len).
	win := min(len(want), Rate) // up to 1s of stimulus
	ref := want[:win]

	refEnergy := energy(ref)
	if refEnergy <= 0 {
		return 0, 0
	}

	bestLag, bestScore := 0, 0.0
	for lag := 0; lag <= maxLagSamples && lag+win <= len(got); lag++ {
		seg := got[lag : lag+win]
		segEnergy := energy(seg)
		if segEnergy <= 0 {
			continue
		}
		var dot float64
		for i := range ref {
			dot += float64(ref[i]) * float64(seg[i])
		}
		// Normalized cross-correlation: comparable across gain differences, so
		// a quiet mic does not read as a worse match than a loud one.
		score := math.Abs(dot) / math.Sqrt(refEnergy*segEnergy)
		if score > bestScore {
			bestScore, bestLag = score, lag
		}
	}
	return bestLag, bestScore
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
