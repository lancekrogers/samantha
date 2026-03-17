package audio

import (
	"math"
	"sync"
	"time"
)

const (
	frontendTargetRMS = 0.08
	playbackHalfLife  = 160 * time.Millisecond
	highPassAlpha     = 0.995
)

// VoiceFrontend applies lightweight local preprocessing to capture audio.
// It reduces rumble, gates low-level noise, ducks likely speaker bleed using
// playback reference energy, and stabilizes level with a gentle AGC.
type VoiceFrontend struct {
	mu sync.Mutex

	lastCaptureX float64
	lastCaptureY float64
	noiseFloor   float64
	gain         float64

	playbackEnvelope float64
	lastPlaybackAt   time.Time
}

// NewVoiceFrontend creates the default local audio front-end.
func NewVoiceFrontend() *VoiceFrontend {
	return &VoiceFrontend{
		noiseFloor: 0.0025,
		gain:       1.0,
	}
}

// ProcessCapture cleans incoming microphone samples in place for VAD/STT use.
func (f *VoiceFrontend) ProcessCapture(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	playbackEnvelope := f.playbackLevelLocked(time.Now())
	out := make([]float32, len(samples))

	sumSquares := 0.0
	for i, sample := range samples {
		x := float64(sample)
		y := highPassAlpha * (f.lastCaptureY + x - f.lastCaptureX)
		f.lastCaptureX = x
		f.lastCaptureY = y
		out[i] = float32(y)
		sumSquares += y * y
	}

	rms := math.Sqrt(sumSquares / float64(len(out)))
	if rms < f.noiseFloor*1.5 {
		f.noiseFloor = 0.995*f.noiseFloor + 0.005*rms
	} else {
		f.noiseFloor = 0.999*f.noiseFloor + 0.001*rms
	}

	gateThreshold := math.Max(f.noiseFloor*1.8, 0.0025)
	duck := 1.0
	if playbackEnvelope > gateThreshold*2 {
		ratio := rms / math.Max(playbackEnvelope, 1e-6)
		switch {
		case ratio < 0.55:
			duck = 0.18
		case ratio < 0.9:
			duck = 0.35
		default:
			duck = 0.7
		}
	}

	postSquares := 0.0
	for i, sample := range out {
		value := float64(sample) * duck
		abs := math.Abs(value)
		if abs < gateThreshold {
			value *= 0.15
		}
		out[i] = float32(value)
		postSquares += value * value
	}

	postRMS := math.Sqrt(postSquares / float64(len(out)))
	targetGain := clamp(frontendTargetRMS/math.Max(postRMS, 1e-4), 0.8, 3.0)
	if targetGain > f.gain {
		f.gain = 0.25*f.gain + 0.75*targetGain
	} else {
		f.gain = 0.92*f.gain + 0.08*targetGain
	}

	for i, sample := range out {
		value := float64(sample) * f.gain
		out[i] = float32(clamp(value, -1.0, 1.0))
	}

	return out
}

// PushPlaybackReference updates the reference energy used to duck speaker bleed.
func (f *VoiceFrontend) PushPlaybackReference(samples []float32) {
	if len(samples) == 0 {
		return
	}

	sumSquares := 0.0
	for _, sample := range samples {
		value := float64(sample)
		sumSquares += value * value
	}
	rms := math.Sqrt(sumSquares / float64(len(samples)))

	f.mu.Lock()
	defer f.mu.Unlock()

	current := f.playbackLevelLocked(time.Now())
	if rms > current {
		f.playbackEnvelope = 0.4*current + 0.6*rms
	} else {
		f.playbackEnvelope = 0.85*current + 0.15*rms
	}
	f.lastPlaybackAt = time.Now()
}

// Close releases front-end resources.
func (f *VoiceFrontend) Close() error {
	return nil
}

func (f *VoiceFrontend) playbackLevelLocked(now time.Time) float64 {
	if f.lastPlaybackAt.IsZero() {
		return f.playbackEnvelope
	}
	elapsed := now.Sub(f.lastPlaybackAt)
	if elapsed <= 0 {
		return f.playbackEnvelope
	}

	decay := math.Exp(-math.Ln2 * float64(elapsed) / float64(playbackHalfLife))
	f.playbackEnvelope *= decay
	f.lastPlaybackAt = now
	return f.playbackEnvelope
}

func clamp[T ~float64 | ~float32](value, low, high T) T {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
