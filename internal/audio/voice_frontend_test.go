package audio

import (
	"math"
	"testing"
)

func TestVoiceFrontendDucksPlaybackBleed(t *testing.T) {
	frontend := NewVoiceFrontend()

	playback := make([]float32, 512)
	for i := range playback {
		playback[i] = voicedSample(i, 0.4)
	}
	frontend.PushPlaybackReference(playback)

	capture := make([]float32, 512)
	for i := range capture {
		capture[i] = voicedSample(i, 0.18)
	}

	processed := frontend.ProcessCapture(capture)
	before := meanAbs(capture)
	after := meanAbs(processed)
	if after >= before {
		t.Fatalf("meanAbs(processed) = %.4f, want less than %.4f", after, before)
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

func voicedSample(i int, amplitude float32) float32 {
	t := float64(i) / float64(SampleRate)
	wave := math.Sin(2*math.Pi*190*t) + 0.4*math.Sin(2*math.Pi*260*t)
	return amplitude * float32(wave/1.4)
}
