package audio

import "testing"

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

func TestVoiceFrontendBoostsQuietSpeech(t *testing.T) {
	frontend := NewVoiceFrontend()

	quiet := make([]float32, 512)
	for i := range quiet {
		quiet[i] = voicedSample(i, 0.01)
	}

	processed := frontend.ProcessCapture(quiet)
	if got := meanAbs(processed); got <= meanAbs(quiet) {
		t.Fatalf("meanAbs(processed) = %.4f, want greater than %.4f", got, meanAbs(quiet))
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
	switch i % 8 {
	case 0, 1:
		return amplitude
	case 2, 3:
		return amplitude * 0.5
	case 4, 5:
		return -amplitude
	default:
		return -amplitude * 0.5
	}
}
