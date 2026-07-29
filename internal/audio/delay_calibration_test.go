package audio

import (
	"math"
	"testing"
)

// The field case, end to end. On a MacBook's built-in speakers the true
// reference-to-echo lag is 102.13ms while the player's ring-derived estimate is
// 42.63ms. The 59.5ms residual does not fit the 48ms tap window and measured
// ERLE was -1.15dB — the canceller doing nothing at all.
//
// Feeding the same geometry through the front-end, calibration has to find the
// lag on its own and recover the cancellation.
func TestCalibrationRecoversCancellationAtFieldLatency(t *testing.T) {
	trueLagMS, staticEstMS := 102.13, 42.63
	trueLag := int(trueLagMS * float64(SampleRate) / 1000)
	staticEst := int(staticEstMS * float64(SampleRate) / 1000)

	uncalibrated := runEchoPath(t, trueLag, staticEst, false)
	calibrated := runEchoPath(t, trueLag, staticEst, true)
	// The absolute ceiling: the offset a perfect measurement would produce.
	// Asserting against this rather than a fixed number keeps the test honest
	// about what is achievable at this geometry — the filter cannot exceed it,
	// so a hard-coded target would either be unreachable or meaningless.
	ceiling := runEchoPath(t, trueLag, trueLag, false)

	t.Logf("static %.2fms → %+.2f dB; calibrated → %+.2f dB; perfect alignment → %+.2f dB",
		staticEstMS, uncalibrated, calibrated, ceiling)

	if uncalibrated > 4 {
		t.Fatalf("uncalibrated ERLE %+.2f dB; the test no longer reproduces the "+
			"field failure it exists to guard", uncalibrated)
	}
	if calibrated < uncalibrated+3 {
		t.Errorf("calibration bought only %+.2f dB (%.2f → %.2f); it is not finding the lag",
			calibrated-uncalibrated, uncalibrated, calibrated)
	}
	// The gap to the ceiling is the safety margin deliberately subtracted from
	// every measurement, so it should be small and stable.
	if gap := ceiling - calibrated; gap > 2.5 {
		t.Errorf("calibrated ERLE %+.2f dB is %.2f dB short of the %+.2f dB ceiling; "+
			"more than the safety margin should cost", calibrated, gap, ceiling)
	}
}

// runEchoPath drives a front-end with far-end audio and a delayed echo of it,
// returning ERLE measured against the same chain with no reference fed.
func runEchoPath(t *testing.T, echoLag, staticEstimate int, allowCalibration bool) float64 {
	t.Helper()

	const chunk = SampleRate / 100 // 10ms capture callbacks
	total := SampleRate * 12
	far := farEndSpeech(total+echoLag, 1)

	run := func(feedRef bool) float64 {
		f := NewVoiceFrontend()
		if !allowCalibration {
			f.pinReferenceDelay()
		}
		f.SetReferenceDelay(staticEstimate)

		var energy float64
		for start := 0; start+chunk <= total; start += chunk {
			if feedRef {
				f.PushPlaybackReference(far[start : start+chunk])
			}
			mic := make([]float32, chunk)
			for i := range mic {
				if src := start + i - echoLag; src >= 0 {
					mic[i] = 0.5 * far[src]
				}
			}
			out := f.ProcessCapture(mic)
			// Score the back half, after the filter has adapted to whatever
			// offset it ended up with.
			if start > total/2 {
				for _, s := range out {
					energy += float64(s) * float64(s)
				}
			}
		}
		return energy
	}

	withRef, withoutRef := run(true), run(false)
	return 10 * math.Log10(withoutRef/math.Max(withRef, 1e-12))
}

// Over-estimating the delay is the one error a longer filter cannot absorb: it
// asks the canceller to predict the microphone from reference audio that has
// not been pushed yet. Every guard against it has to hold.
func TestEstimateReferenceDelayRefusesTheUnrecoverableDirection(t *testing.T) {
	static := 682 // the field estimate, 42.63ms

	// A measurement below the known-safe floor is rejected, not adopted.
	if _, ok := estimateReferenceDelay(400, 0.9, static); ok {
		t.Error("adopted a measurement below the static floor")
	}

	// A confident measurement above it is adopted, minus the safety margin.
	got, ok := estimateReferenceDelay(1634, 0.9, static)
	if !ok {
		t.Fatal("rejected a confident measurement above the floor")
	}
	if want := 1634 - calibrationSafetyMargin; got != want {
		t.Errorf("adopted %d, want %d (measurement minus the safety margin)", got, want)
	}
	if got >= 1634 {
		t.Error("adopted delay must stay under the measurement, never above it")
	}

	// Low confidence is never believed, however plausible the number.
	if _, ok := estimateReferenceDelay(1634, calibrationMinConfidence-0.01, static); ok {
		t.Error("adopted a low-confidence measurement")
	}

	// Absurd lags are rejected rather than clamped.
	if _, ok := estimateReferenceDelay(calibrationMaxLag*2, 0.9, static); ok {
		t.Error("adopted a lag beyond the search range")
	}
}

// Without a believable measurement the front-end must behave exactly as it does
// today. Calibration is an improvement, not a new failure mode.
func TestUncalibratedFrontendKeepsTheStaticEstimate(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(682)

	// Silence carries no correlation peak, so no measurement should stick.
	for range 400 {
		f.PushPlaybackReference(make([]float32, 160))
		f.ProcessCapture(make([]float32, 160))
	}

	delay, measured := f.ReferenceDelay()
	if measured {
		t.Error("claimed a measured delay from silence")
	}
	if delay != 682 {
		t.Errorf("delay = %d, want the static estimate 682 retained", delay)
	}
}

// The measurement describes one speaker-and-microphone path. Switching devices
// invalidates it, and the front-end has to fall back rather than carry a delay
// that belonged to different hardware.
func TestResetCalibrationReturnsToTheStaticEstimate(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(682)

	f.applyReferenceDelay(1614)
	f.calibrate.noteApplied(1614)
	if delay, _ := f.ReferenceDelay(); delay != 1614 {
		t.Fatalf("setup failed: delay = %d", delay)
	}

	f.ResetCalibration()

	delay, measured := f.ReferenceDelay()
	if measured {
		t.Error("still reporting a measured delay after reset")
	}
	if delay != 682 {
		t.Errorf("delay = %d after reset, want the static estimate 682", delay)
	}
}

// A device-rate change must not silently discard a measurement of the live
// path, which is strictly better information than the ring-derived estimate.
func TestStaticEstimateDoesNotOverwriteAMeasurement(t *testing.T) {
	f := NewVoiceFrontend()
	f.SetReferenceDelay(682)

	f.applyReferenceDelay(1614)
	f.calibrate.noteApplied(1614)

	f.SetReferenceDelay(700) // e.g. renegotiated device rate
	if delay, _ := f.ReferenceDelay(); delay != 1614 {
		t.Errorf("delay = %d, want the measured 1614 to survive a smaller static estimate", delay)
	}

	// But a larger static estimate than the measurement means the measurement
	// is suspect, and the safe floor should win.
	f.SetReferenceDelay(2000)
	if delay, _ := f.ReferenceDelay(); delay != 2000 {
		t.Errorf("delay = %d, want the larger static estimate 2000 to win", delay)
	}
}

// The field failure the simulation originally missed. A synthetic echo is one
// clean scaled copy, which correlates sharply and flatters the estimator. Real
// echo arrives as a direct path plus reflections, buried in room noise, and
// speech correlates with itself at many lags — a run in the field adopted
// 45.69ms against a true 100.06ms because a broad peak cleared an absolute
// threshold in the wrong place.
func TestCorrelateDelayFindsTheDirectPathThroughReverb(t *testing.T) {
	trueLagMS := 100.06
	trueLag := int(trueLagMS * float64(SampleRate) / 1000)

	total := SampleRate * 3
	far := farEndSpeech(total+trueLag+SampleRate, 1)
	noise := farEndSpeech(total+trueLag+SampleRate, 77)

	// Direct path plus decaying reflections at realistic spacings, then a room
	// noise floor. Reflections are what smear the correlation.
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

	lag, confidence := CorrelateDelay(far[:calibrationWindow], mic, calibrationMaxLag, calibrationWindow)
	errMS := float64(lag-trueLag) * 1000 / float64(SampleRate)
	t.Logf("true %.2fms, found %.2fms (error %+.2fms), confidence %.3f",
		trueLagMS, float64(lag)*1000/float64(SampleRate), errMS, confidence)

	// Within the guard band of the direct path. Landing on a reflection instead
	// would still be usable; landing 55ms early, as the field run did, is not.
	if errMS < -5 || errMS > 15 {
		t.Errorf("found %+.2fms from the direct path; the estimator is locking onto "+
			"a spurious peak the way the field run did", errMS)
	}
	if confidence < calibrationMinConfidence {
		t.Errorf("confidence %.3f is below the %.2f adoption bar, so a correct "+
			"measurement would be thrown away", confidence, calibrationMinConfidence)
	}
}

// Prominence, not raw correlation, is what the confidence has to express. A
// signal that matches everywhere must not be believed anywhere.
func TestCorrelateDelayRejectsAnAmbiguousPeak(t *testing.T) {
	// A pure tone correlates equally well at every period, so no lag is
	// distinguishable and nothing should be adopted.
	tone := make([]float32, SampleRate)
	for i := range tone {
		tone[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / float64(SampleRate)))
	}

	_, confidence := CorrelateDelay(tone[:calibrationWindow], tone, calibrationMaxLag, calibrationWindow)
	t.Logf("periodic-signal confidence %.3f", confidence)
	if confidence >= calibrationMinConfidence {
		t.Errorf("confidence %.3f on a signal that matches at every period; a broad "+
			"peak must not clear the adoption bar", confidence)
	}
}
