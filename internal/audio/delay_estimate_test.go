package audio

import (
	"math"
	"testing"
)

const (
	testCorrWindow = SampleRate / 4     // 250ms
	testCorrMaxLag = SampleRate * 2 / 5 // 400ms
)

// The failure this exists to prevent. A synthetic echo is one clean scaled
// copy, which correlates sharply and flatters the estimator; real echo is a
// direct path plus reflections in room noise, and speech correlates with itself
// at many lags. A hardware run reported 45.69ms against a true 100.06ms because
// a broad peak cleared an absolute threshold in the wrong place — and, worse,
// four earlier runs agreed with each other at ~100ms while sharing the same
// bias, which read as confirmation.
func TestCorrelateDelayFindsTheDirectPathThroughReverb(t *testing.T) {
	trueLagMS := 100.06
	trueLag := int(trueLagMS * float64(SampleRate) / 1000)

	total := SampleRate * 3
	far := reverbTestSpeech(total+trueLag+SampleRate, 1)
	noise := reverbTestSpeech(total+trueLag+SampleRate, 77)

	// Direct path plus decaying reflections at realistic spacings, then a room
	// noise floor. The reflections are what smear the correlation.
	reflections := []struct {
		delayMS float64
		gain    float32
	}{{0, 0.5}, {7, 0.28}, {13, 0.19}, {23, 0.12}, {37, 0.07}, {53, 0.04}}

	mic := make([]float32, total+trueLag+SampleRate)
	for i := range mic {
		var sum float32
		for _, r := range reflections {
			src := i - trueLag - int(r.delayMS*float64(SampleRate)/1000)
			if src >= 0 && src < len(far) {
				sum += r.gain * far[src]
			}
		}
		mic[i] = sum + 0.02*noise[i]
	}

	lag, confidence := CorrelateDelay(far[:testCorrWindow], mic, testCorrMaxLag, testCorrWindow)
	errMS := float64(lag-trueLag) * 1000 / float64(SampleRate)
	t.Logf("true %.2fms, found %.2fms (error %+.2fms), confidence %.3f",
		trueLagMS, float64(lag)*1000/float64(SampleRate), errMS, confidence)

	// Landing on an early reflection is still usable; landing 55ms early, as
	// the field run did, is not.
	if errMS < -5 || errMS > 15 {
		t.Errorf("found %+.2fms from the direct path; the estimator is locking onto "+
			"a spurious peak the way the field run did", errMS)
	}
	if confidence < 0.2 {
		t.Errorf("confidence %.3f on a correct measurement; a real room must not "+
			"score so low that good measurements get discarded", confidence)
	}
}

// Prominence, not raw correlation, is what confidence has to express. A signal
// that matches everywhere must not be believed anywhere.
func TestCorrelateDelayRejectsAnAmbiguousPeak(t *testing.T) {
	tone := make([]float32, SampleRate)
	for i := range tone {
		tone[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / float64(SampleRate)))
	}

	_, confidence := CorrelateDelay(tone[:testCorrWindow], tone, testCorrMaxLag, testCorrWindow)
	t.Logf("periodic-signal confidence %.3f", confidence)
	if confidence >= 0.2 {
		t.Errorf("confidence %.3f on a signal that matches at every period; a broad "+
			"peak must not look like a measurement", confidence)
	}
}

// A clean planted delay must come back exactly, or the probe's headline number
// is untrustworthy before any room is involved.
func TestCorrelateDelayRecoversPlantedLags(t *testing.T) {
	stimulus := reverbTestSpeech(testCorrWindow, 3)

	for _, planted := range []int{0, 37, 160, 512, 1024, 3200} {
		recording := make([]float32, planted+len(stimulus)+SampleRate)
		for i, s := range stimulus {
			recording[planted+i] = 0.4 * s
		}
		lag, confidence := CorrelateDelay(stimulus, recording, testCorrMaxLag, testCorrWindow)
		if lag != planted {
			t.Errorf("planted %d, measured %d", planted, lag)
		}
		if confidence < 0.5 {
			t.Errorf("planted %d: confidence %.3f on a clean echo, want a clear peak",
				planted, confidence)
		}
	}
}

// Silence must never produce a confident answer — that is what keeps a muted
// speaker or a dead microphone from being recorded as a delay.
func TestCorrelateDelayReportsNothingOnSilence(t *testing.T) {
	stimulus := reverbTestSpeech(testCorrWindow, 5)
	if _, confidence := CorrelateDelay(stimulus, make([]float32, 3*SampleRate), testCorrMaxLag, testCorrWindow); confidence != 0 {
		t.Errorf("confidence %.3f against silence, want 0", confidence)
	}
}

// reverbTestSpeech is speech-shaped: band-limited noise under a syllable-rate
// envelope. White noise would flatter the correlator by being easy to align.
func reverbTestSpeech(n int, seed int64) []float32 {
	out := make([]float32, n)
	state := uint64(seed)
	if state == 0 {
		state = 0x9E3779B97F4A7C15
	}
	var lp float64
	for i := range out {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		u := float64(state>>11)/float64(1<<53)*2 - 1
		lp = 0.85*lp + 0.15*u
		env := 0.5 + 0.5*math.Sin(2*math.Pi*float64(i)/float64(SampleRate)*3)
		out[i] = float32(lp * env * 0.6)
	}
	return out
}
