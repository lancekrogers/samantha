package audio

import "math"

// Runtime measurement of the far-end reference delay.
//
// referenceDelaySamples infers output latency from the miniaudio ring —
// (Periods-1)*PeriodSizeInFrames — because that is all the player can see. On a
// MacBook's built-in speakers the measured lag is 102ms against a 42.6ms
// inferred estimate: CoreAudio adds roughly 60ms the ring does not describe.
// That 59ms residual does not fit the 48ms tap window, and the canceller
// delivers nothing (-1.15dB measured; the AGC's makeup gain slightly exceeds
// what the suppressor removes).
//
// The estimate cannot be corrected by reasoning about buffers, because the
// missing latency lives in the driver. It has to be measured against the
// microphone, which is what this does: cross-correlate the reference that was
// pushed against the raw microphone that heard its echo.

// CorrelateDelay finds the lag, in samples, that best aligns want inside got.
// Shared by the runtime calibrator and the hardware probe so both agree on what
// "the delay" means.
//
// The confidence returned is peak *prominence*, not raw correlation. Speech
// correlates strongly with itself at many lags, so a raw score says only "these
// signals are related", which is never in doubt — the echo is the far-end by
// construction. A run in the field adopted 45.69ms against a true 100.06ms
// precisely because a broad peak cleared an absolute threshold at the wrong
// place. What matters is whether one lag stands out from its neighbours.
func CorrelateDelay(want, got []float32, maxLag, window int) (lag int, confidence float64) {
	if len(want) == 0 || len(got) == 0 || maxLag <= 0 || window <= 0 {
		return 0, 0
	}
	if window > len(want) {
		window = len(want)
	}

	// Whiten both sides with a first difference. Speech-shaped audio is heavily
	// low-frequency weighted, which smears the correlation across tens of
	// milliseconds; flattening the spectrum sharpens the peak without needing
	// an FFT.
	ref := whiten(want[:window])
	refEnergy := sumSquares(ref)
	if refEnergy <= 0 {
		return 0, 0
	}
	hay := whiten(got)

	bestLag, bestScore, runnerUp := 0, 0.0, 0.0
	scores := make([]float64, 0, maxLag+1)
	for lag := 0; lag <= maxLag && lag+window <= len(hay); lag++ {
		seg := hay[lag : lag+window]
		segEnergy := sumSquares(seg)
		if segEnergy <= 0 {
			scores = append(scores, 0)
			continue
		}
		var dot float64
		for i := range ref {
			dot += float64(ref[i]) * float64(seg[i])
		}
		// Normalized, so a quiet microphone does not score worse than a loud one.
		score := math.Abs(dot) / math.Sqrt(refEnergy*segEnergy)
		scores = append(scores, score)
		if score > bestScore {
			bestScore, bestLag = score, lag
		}
	}
	if bestScore <= 0 {
		return 0, 0
	}

	// Runner-up outside a guard band around the peak. A true acoustic peak has
	// close neighbours — the echo is not a single impulse — so only lags well
	// clear of it count as competitors.
	guard := SampleRate / 200 // 5ms
	for i, score := range scores {
		if i >= bestLag-guard && i <= bestLag+guard {
			continue
		}
		if score > runnerUp {
			runnerUp = score
		}
	}

	// Prominence: 1 when nothing else comes close, 0 when the peak is one of
	// many. Scaled by the raw score so a distinct peak in noise still reads low.
	prominence := (bestScore - runnerUp) / bestScore
	return bestLag, bestScore * prominence
}

// whiten applies a first-difference high-pass, flattening the spectral tilt
// that makes speech correlate broadly with itself.
func whiten(samples []float32) []float32 {
	if len(samples) < 2 {
		return samples
	}
	out := make([]float32, len(samples))
	for i := 1; i < len(samples); i++ {
		out[i] = samples[i] - samples[i-1]
	}
	return out
}

func sumSquares(samples []float32) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return sum
}

const (
	// calibrationWindow is how much contiguous reference audio one estimate
	// correlates. Long enough that speech-like far-end has a distinct
	// signature, short enough that the O(maxLag*window) search stays cheap
	// enough to run on a worker.
	calibrationWindow = SampleRate / 4 // 250ms
	// calibrationMaxLag bounds the search. 400ms covers built-in speakers
	// (~102ms measured) with room for slower paths; anything past this is
	// beyond what any tap budget could span anyway.
	calibrationMaxLag = SampleRate * 2 / 5 // 400ms
	// calibrationMinConfidence is the bar a peak's prominence must clear to be
	// believed. Because confidence now expresses distinctness rather than raw
	// correlation, the separation is wide: a reverberant room with a noise floor
	// scores ~0.35 at the true lag, while a signal that matches at every period
	// scores 0.000. The bar sits below the former with headroom for a noisier
	// room, and far above the latter.
	calibrationMinConfidence = 0.25
	// calibrationSafetyMargin is subtracted from every measurement before it is
	// applied. The asymmetry is the whole point: an under-estimate costs taps,
	// which the filter has, while an over-estimate asks it to predict the
	// microphone from reference audio that has not been pushed yet, which no
	// filter length can fix. Observed run-to-run scatter was ~6ms; this is
	// comfortably past it.
	calibrationSafetyMargin = SampleRate / 50 // 20ms
	// calibrationGapSamples is how much silence ends a collection window. The
	// correlation assumes the reference it holds is contiguous; a pause in
	// playback breaks that, so the window restarts.
	calibrationGapSamples = SampleRate / 10 // 100ms
)

// estimateReferenceDelay converts a raw correlation into the offset to apply,
// or reports that the measurement should not be believed.
//
// staticEstimate is the player's ring-derived floor. A measurement below it is
// rejected rather than applied: the floor is known-safe by construction, and
// adopting something smaller would risk the unrecoverable direction.
func estimateReferenceDelay(measured int, confidence float64, staticEstimate int) (delay int, ok bool) {
	if confidence < calibrationMinConfidence {
		return 0, false
	}
	delay = measured - calibrationSafetyMargin
	if delay < staticEstimate {
		// Either the driver adds nothing beyond the ring, or the measurement is
		// wrong. Both are handled by keeping the estimate we already trust.
		return 0, false
	}
	if delay > calibrationMaxLag {
		return 0, false
	}
	return delay, true
}
