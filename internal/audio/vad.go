package audio

import (
	"fmt"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/samantha/internal/config"
)

// VAD wraps sherpa-onnx Silero voice activity detection.
type VAD struct {
	detector *sherpa.VoiceActivityDetector
}

// NewVAD creates a VAD instance with the Silero model.
func NewVAD(cfg *config.Config) (*VAD, error) {
	modelPath := filepath.Join(config.ModelsDir(), "silero_vad.onnx")

	sileroConfig := sherpa.SileroVadModelConfig{
		Model:              modelPath,
		MinSpeechDuration:  0.25,
		MinSilenceDuration: float32(cfg.VADSilenceDuration),
		Threshold:          0.6,
	}

	vadConfig := sherpa.VadModelConfig{
		SileroVad:  sileroConfig,
		SampleRate: SampleRate,
	}

	detector := sherpa.NewVoiceActivityDetector(&vadConfig, 30) // 30s buffer
	if detector == nil {
		return nil, fmt.Errorf("failed to create VAD detector (model: %s)", modelPath)
	}

	return &VAD{detector: detector}, nil
}

// AcceptWaveform feeds audio samples to the VAD.
func (v *VAD) AcceptWaveform(samples []float32) {
	v.detector.AcceptWaveform(samples)
}

// IsSpeechDetected returns true if speech has been detected and segments are available.
func (v *VAD) IsSpeechDetected() bool {
	return !v.detector.IsEmpty()
}

// IsEmpty returns true if no speech segments are available.
func (v *VAD) IsEmpty() bool {
	return v.detector.IsEmpty()
}

// IsSpeech returns true if current audio chunk contains speech.
func (v *VAD) IsSpeech() bool {
	return v.detector.IsSpeech()
}

// Front returns the first detected speech segment's samples.
func (v *VAD) Front() []float32 {
	seg := v.detector.Front()
	return seg.Samples
}

// Pop removes the first speech segment.
func (v *VAD) Pop() {
	v.detector.Pop()
}

// Clear resets the VAD state.
func (v *VAD) Clear() {
	v.detector.Clear()
}

// Flush signals the VAD that the audio stream has ended.
func (v *VAD) Flush() {
	v.detector.Flush()
}

// Delete frees the VAD resources.
func (v *VAD) Delete() {
	sherpa.DeleteVoiceActivityDetector(v.detector)
}
