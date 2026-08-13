package audio

import (
	"math"
	"math/rand"
	"testing"
)

func TestVoiceFrontendCancelsEchoReference(t *testing.T) {
	frontend := NewVoiceFrontend()

	playback := make([]float32, 512)
	for i := range playback {
		playback[i] = voicedSample(i, 0.4)
	}
	capture := make([]float32, len(playback))
	for i := range capture {
		capture[i] = playback[i]*0.65 + voicedSample(i, 0.04)
	}

	var processed []float32
	for range 10 {
		frontend.PushPlaybackReference(playback)
		processed = frontend.ProcessCapture(capture)
	}

	if got, want := correlation(playback, processed), correlation(playback, capture); math.Abs(got) >= math.Abs(want)*0.55 {
		t.Fatalf("echo correlation = %.4f, want < %.4f after cancellation", got, math.Abs(want)*0.55)
	}
}

func TestVoiceFrontendPreservesQuietSpeechAboveNoiseFloor(t *testing.T) {
	frontend := NewVoiceFrontend()

	quiet := make([]float32, 512)
	for i := range quiet {
		quiet[i] = voicedSample(i, 0.03)
	}

	var processed []float32
	for range 6 {
		processed = frontend.ProcessCapture(quiet)
	}
	if got := meanAbs(processed); got <= 0.001 {
		t.Fatalf("meanAbs(processed) = %.4f, want > 0.001 after frontend processing", got)
	}
}

func TestVoiceFrontendSuppressesLowLevelNoise(t *testing.T) {
	frontend := NewVoiceFrontend()
	noise := make([]float32, 512)
	rng := rand.New(rand.NewSource(42))
	for i := range noise {
		noise[i] = float32(rng.NormFloat64() * 0.002)
	}

	processed := noise
	for range 8 {
		processed = frontend.ProcessCapture(processed)
	}

	if got := meanAbs(processed); got >= meanAbs(noise)*0.75 {
		t.Fatalf("meanAbs(processed) = %.4f, want less than %.4f for noise suppression", got, meanAbs(noise)*0.75)
	}
}

func meanAbs(samples []float32) float64 {
	sum := 0.0
	for _, sample := range samples {
		if sample < 0 {
			sum -= float64(sample)
			continue
		}
		sum += float64(sample)
	}
	return sum / float64(len(samples))
}

func correlation(a, b []float32) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}

	dot := 0.0
	ma := 0.0
	mb := 0.0
	for i := range n {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		ma += av * av
		mb += bv * bv
	}
	if ma == 0 || mb == 0 {
		return 0
	}
	return dot / math.Sqrt(ma*mb)
}

func voicedSample(i int, amplitude float32) float32 {
	t := float64(i) / float64(SampleRate)
	wave := math.Sin(2*math.Pi*190*t) + 0.4*math.Sin(2*math.Pi*260*t)
	return amplitude * float32(wave/1.4)
}
