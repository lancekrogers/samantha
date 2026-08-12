package audio

import "math"

// Cross-correlation of the far-end reference against the microphone that heard
// its echo, used by the hardware probe to measure true output latency.
//
// referenceDelaySamples infers that latency from the miniaudio ring, because
// that is all the player can see. Whether the inference is right is only
// answerable by measuring against a real speaker and microphone, and the
// measurement is only worth anything if its confidence means something.

// CorrelateDelay finds the lag, in samples, that best aligns want inside got.
//
// The confidence returned is peak *prominence*, not raw correlation. Speech
// correlates strongly with itself at many lags, so a raw score says only "these
// signals are related", which is never in doubt — the echo is the far-end by
// construction. A run in the field adopted 45.69ms against a true 100.06ms
// precisely because a broad peak cleared an absolute threshold at the wrong
// place. What matters is whether one lag stands out from its neighbours.
//
// Exported so the probe in internal/aecprobe uses exactly this, rather than a
// second implementation that could drift from it.
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
