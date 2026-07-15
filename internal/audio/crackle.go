package audio

import (
	"encoding/binary"
	"math"
	"sort"
)

// CrackleMetrics scores mono PCM for software-detectable crackle signatures.
//
// Audible crackle has multiple root causes (sample-rate conversion, mid-speech
// underruns, chunk-boundary discontinuities). CI cannot hear CoreAudio, but it
// can reject the pre-backend defects that historically produced the same
// symptom. See docs/guides/troubleshooting/samantha-audio-corruption.md.
type CrackleMetrics struct {
	SampleCount int

	// MaxAbsDelta is the largest absolute sample-to-sample jump (float, |x|≤1).
	MaxAbsDelta float64
	// P99AbsDelta is the 99th-percentile absolute jump.
	P99AbsDelta float64
	// ImpulseCount is the number of jumps that exceed the local envelope
	// threshold and are therefore click-like rather than ordinary speech motion.
	ImpulseCount int

	// MidSilenceFrames counts near-zero samples that sit between non-silent
	// speech on both sides — the classic underrun / gap signature.
	MidSilenceFrames int
	// MidSilenceRuns is the number of distinct mid-speech silence holes.
	MidSilenceRuns int
}

// CrackleThresholds control when metrics are treated as a regression.
// Zero fields use the package defaults tuned for speech-like test signals.
type CrackleThresholds struct {
	// MaxImpulses is the maximum allowed impulse count. Default: 0.
	MaxImpulses int
	// MaxMidSilenceRuns is the maximum allowed mid-speech silence holes.
	// Default: 0 (any inserted gap fails).
	MaxMidSilenceRuns int
	// MaxMidSilenceFrames is the total mid-speech silence budget. Default: 0.
	MaxMidSilenceFrames int
	// ImpulseFactor multiplies the recent mean sample-to-sample delta when
	// classifying impulses. Default: 8.
	ImpulseFactor float64
	// MinImpulseDelta is the absolute jump floor for impulse classification.
	// Band-limited speech at ≥24 kHz rarely exceeds ~0.15 between adjacent
	// samples; clicks and boundary glitches are much larger. Default: 0.35.
	MinImpulseDelta float64
	// SilenceAbs is the absolute float amplitude treated as silence. Default: 1/512.
	SilenceAbs float64
	// MinSpeechAbs is the absolute amplitude that counts as speech when
	// bookending a silence hole. Default: 1/64.
	MinSpeechAbs float64
}

func (t CrackleThresholds) withDefaults() CrackleThresholds {
	if t.ImpulseFactor <= 0 {
		t.ImpulseFactor = 8
	}
	if t.MinImpulseDelta <= 0 {
		t.MinImpulseDelta = 0.35
	}
	if t.SilenceAbs <= 0 {
		t.SilenceAbs = 1.0 / 512.0
	}
	if t.MinSpeechAbs <= 0 {
		t.MinSpeechAbs = 1.0 / 64.0
	}
	// MaxImpulses / mid-silence budgets intentionally default to 0.
	return t
}

// HasCrackle reports whether the metrics exceed thresholds.
func (m CrackleMetrics) HasCrackle(t CrackleThresholds) bool {
	t = t.withDefaults()
	if m.ImpulseCount > t.MaxImpulses {
		return true
	}
	if m.MidSilenceRuns > t.MaxMidSilenceRuns {
		return true
	}
	if m.MidSilenceFrames > t.MaxMidSilenceFrames {
		return true
	}
	return false
}

// AnalyzeFloat32 scores mono float32 samples in [-1, 1].
func AnalyzeFloat32(samples []float32, t CrackleThresholds) CrackleMetrics {
	t = t.withDefaults()
	m := CrackleMetrics{SampleCount: len(samples)}
	if len(samples) < 2 {
		return m
	}

	deltas := make([]float64, 0, len(samples)-1)
	// Recent mean |Δ| computed from prior samples only so a click cannot
	// inflate the baseline used to classify it.
	var deltaWindow [64]float64
	deltaFill := 0
	deltaIdx := 0
	var deltaSum float64

	for i := 1; i < len(samples); i++ {
		d := math.Abs(float64(samples[i] - samples[i-1]))
		deltas = append(deltas, d)
		if d > m.MaxAbsDelta {
			m.MaxAbsDelta = d
		}

		var meanDelta float64
		if deltaFill > 0 {
			meanDelta = deltaSum / float64(deltaFill)
		}
		// Floor keeps a quiet prelude from treating the first real speech
		// onset as a cascade of impulses.
		if meanDelta < t.SilenceAbs {
			meanDelta = t.SilenceAbs
		}
		// Only score mid-stream clicks. Jumps that leave or enter near-silence
		// are ordinary speech onsets/offsets, not crackle.
		left := math.Abs(float64(samples[i-1]))
		right := math.Abs(float64(samples[i]))
		if left >= t.MinSpeechAbs && right >= t.MinSpeechAbs &&
			d >= t.MinImpulseDelta && d > t.ImpulseFactor*meanDelta {
			m.ImpulseCount++
		}

		if deltaFill < len(deltaWindow) {
			deltaWindow[deltaFill] = d
			deltaSum += d
			deltaFill++
		} else {
			deltaSum -= deltaWindow[deltaIdx]
			deltaWindow[deltaIdx] = d
			deltaSum += d
			deltaIdx = (deltaIdx + 1) % len(deltaWindow)
		}
	}

	if len(deltas) > 0 {
		sorted := append([]float64(nil), deltas...)
		sort.Float64s(sorted)
		idx := int(math.Ceil(0.99*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		m.P99AbsDelta = sorted[idx]
	}

	m.MidSilenceFrames, m.MidSilenceRuns = countMidSilence(samples, t.SilenceAbs, t.MinSpeechAbs)
	return m
}

// AnalyzeInt16 scores mono PCM16 samples.
func AnalyzeInt16(samples []int16, t CrackleThresholds) CrackleMetrics {
	if len(samples) == 0 {
		return CrackleMetrics{}
	}
	f := make([]float32, len(samples))
	for i, s := range samples {
		f[i] = float32(s) / float32(math.MaxInt16)
	}
	return AnalyzeFloat32(f, t)
}

// AnalyzePCM16LE scores little-endian PCM16 bytes (mono).
func AnalyzePCM16LE(buf []byte, t CrackleThresholds) CrackleMetrics {
	if len(buf) < 2 {
		return CrackleMetrics{}
	}
	n := len(buf) / 2
	samples := make([]int16, n)
	for i := range n {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
	return AnalyzeInt16(samples, t)
}

// countMidSilence finds near-zero runs that have speech-level energy both
// before and after them. Leading/trailing silence is ignored — that is normal
// padding, not an underrun.
func countMidSilence(samples []float32, silenceAbs, speechAbs float64) (frames, runs int) {
	n := len(samples)
	if n == 0 {
		return 0, 0
	}

	// First and last speech-level sample indices.
	firstSpeech, lastSpeech := -1, -1
	for i, s := range samples {
		if math.Abs(float64(s)) >= speechAbs {
			if firstSpeech < 0 {
				firstSpeech = i
			}
			lastSpeech = i
		}
	}
	if firstSpeech < 0 || lastSpeech <= firstSpeech {
		return 0, 0
	}

	inHole := false
	holeStart := 0
	for i := firstSpeech; i <= lastSpeech; i++ {
		silent := math.Abs(float64(samples[i])) < silenceAbs
		if silent {
			if !inHole {
				inHole = true
				holeStart = i
			}
			continue
		}
		if inHole {
			// Require a hole long enough to be audible (~0.5 ms at 24 kHz ≈ 12
			// frames; use a fixed 8-sample floor so tests stay rate-agnostic).
			holeLen := i - holeStart
			if holeLen >= 8 {
				frames += holeLen
				runs++
			}
			inHole = false
		}
	}
	return frames, runs
}

// reconstructCallbackPCM drains a fully-buffered, finished segment the way the
// device callback does: fixed-size frames and a pre-cleared output buffer. It
// is intended for post-finalizeSegment analysis; if the segment underruns
// (writeTo returns 0 before input finishes), one silence period is recorded
// and the drain stops so callers never hang waiting on a producer.
func reconstructCallbackPCM(segment *playbackSegment, frameCount int) (pcm []byte, partialCallbacks int, totalSilenceFrames int) {
	if frameCount <= 0 {
		frameCount = 256
	}
	out := make([]byte, frameCount*2)
	for {
		clearBytes(out)
		written, finished := segment.writeTo(out, frameCount)
		silence := frameCount - written
		if written > 0 && silence > 0 {
			partialCallbacks++
			totalSilenceFrames += silence
		}
		if written == 0 {
			if finished {
				return pcm, partialCallbacks, totalSilenceFrames
			}
			// Underrun: the real callback emits silence and returns. Record one
			// period so crackle analysis can see the hole, then stop — this
			// helper does not block on a live producer.
			pcm = append(pcm, make([]byte, frameCount*2)...)
			totalSilenceFrames += frameCount
			return pcm, partialCallbacks, totalSilenceFrames
		}
		pcm = append(pcm, out[:written*2]...)
		if finished {
			return pcm, partialCallbacks, totalSilenceFrames
		}
	}
}
