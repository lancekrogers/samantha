package aecprobe

import (
	"math"
	"testing"
)

// The probe's whole value is that its delay number is trustworthy. If
// MeasureDelay is wrong, a hardware session produces confident nonsense — worse
// than no measurement, because someone would act on it. Plant known delays and
// require them back.
func TestMeasureDelayRecoversKnownLag(t *testing.T) {
	stimulus := Chirp(1.0, Rate)

	for _, planted := range []int{0, 37, 160, 512, 1024, 3200} {
		recording := make([]float32, planted+len(stimulus)+Rate)
		for i, s := range stimulus {
			recording[planted+i] = 0.4 * s // attenuated, like a real speaker path
		}

		got, confidence := MeasureDelay(stimulus, recording, Rate/2)
		if got != planted {
			t.Errorf("planted lag %d, measured %d", planted, got)
		}
		if confidence < 0.9 {
			t.Errorf("planted lag %d: confidence %.3f, want a clear peak", planted, confidence)
		}
	}
}

// Real recordings carry room noise. The peak has to survive it, or every
// hardware run reads as low-confidence and gets discarded.
func TestMeasureDelaySurvivesNoise(t *testing.T) {
	const planted = 640
	stimulus := Chirp(1.0, Rate)
	rng := newDeterministicRNG(7)

	recording := make([]float32, planted+len(stimulus)+Rate)
	for i := range recording {
		recording[i] = float32(rng.normal() * 0.05) // noise floor
	}
	for i, s := range stimulus {
		recording[planted+i] += 0.4 * s
	}

	got, confidence := MeasureDelay(stimulus, recording, Rate/2)
	// Allow a sample of slack: noise can nudge the argmax without making the
	// measurement useless at millisecond resolution.
	if got < planted-1 || got > planted+1 {
		t.Errorf("planted lag %d, measured %d under noise", planted, got)
	}
	if confidence < 0.3 {
		t.Errorf("confidence %.3f under a realistic noise floor, want a usable peak", confidence)
	}
}

// Silence must not produce a confident answer. This is the guard that keeps a
// muted speaker or a dead microphone from being written down as a delay.
func TestMeasureDelayReportsLowConfidenceOnSilence(t *testing.T) {
	stimulus := Chirp(1.0, Rate)
	silence := make([]float32, 3*Rate)

	_, confidence := MeasureDelay(stimulus, silence, Rate/2)
	if confidence != 0 {
		t.Errorf("confidence %.3f against silence, want 0", confidence)
	}
}

func TestERLEMeasuresSuppression(t *testing.T) {
	in := SpeechLikeNoise(1.0, Rate, 3)

	// 20dB of suppression is a factor of 10 in amplitude.
	out := make([]float32, len(in))
	for i, s := range in {
		out[i] = s / 10
	}
	if got := ERLE(in, out); math.Abs(got-20) > 0.5 {
		t.Errorf("ERLE = %.2f dB for a 10x amplitude reduction, want ~20", got)
	}

	// No change is no cancellation.
	if got := ERLE(in, in); math.Abs(got) > 0.01 {
		t.Errorf("ERLE = %.2f dB for an unchanged signal, want ~0", got)
	}

	// A front-end that amplifies (AGC chasing its target on a quiet residual)
	// must read negative, not be clamped away.
	loud := make([]float32, len(in))
	for i, s := range in {
		loud[i] = s * 2
	}
	if got := ERLE(in, loud); got > -5 {
		t.Errorf("ERLE = %.2f dB for an amplified signal, want clearly negative", got)
	}
}

// The chirp has to be usable for correlation: a signal whose autocorrelation is
// broad would place the delay peak ambiguously and the probe would report
// precise, wrong numbers.
func TestChirpHasSharpAutocorrelation(t *testing.T) {
	c := Chirp(1.0, Rate)

	_, peak := MeasureDelay(c, c, 1)
	if peak < 0.99 {
		t.Fatalf("self-correlation at zero lag = %.3f, want ~1", peak)
	}

	// 20ms off, correlation must have collapsed.
	off := 20 * Rate / 1000
	shifted := make([]float32, len(c)+off)
	copy(shifted[off:], c)
	window := min(len(c), Rate)
	var dot, energyA, energyB float64
	for i := range window {
		a, b := float64(c[i]), float64(shifted[i])
		dot += a * b
		energyA += a * a
		energyB += b * b
	}
	sidelobe := math.Abs(dot) / math.Sqrt(math.Max(energyA*energyB, 1e-12))
	if sidelobe > 0.3 {
		t.Errorf("correlation %.3f at a 20ms offset, want a sharp peak (<0.3)", sidelobe)
	}
}

func TestStimulusIsDeterministic(t *testing.T) {
	a := SpeechLikeNoise(0.5, Rate, 42)
	b := SpeechLikeNoise(0.5, Rate, 42)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("sample %d differs; runs on different days must be comparable", i)
		}
	}
	if c := SpeechLikeNoise(0.5, Rate, 43); c[100] == a[100] {
		t.Error("different seeds produced identical audio")
	}
}

// Stimuli must not clip: a stimulus that already reaches full scale drives the
// speaker nonlinear, and then the run measures distortion rather than the
// canceller.
func TestStimuliStayBelowFullScale(t *testing.T) {
	for name, s := range map[string][]float32{
		"chirp": Chirp(1.0, 24000),
		"noise": SpeechLikeNoise(1.0, 24000, 5),
	} {
		if peak := PeakLevel(s); peak >= 0.95 {
			t.Errorf("%s peaks at %.3f; too close to full scale", name, peak)
		}
	}
}
