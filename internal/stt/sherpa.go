package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/config"
)

// SherpaSTT implements STT using sherpa-onnx whisper.
type SherpaSTT struct {
	cfg        *config.Config
	recognizer *sherpa.OfflineRecognizer
	vad        *audio.VAD
	capture    *audio.Capture
}

// NewSherpaSTT creates a new sherpa-onnx whisper STT provider.
func NewSherpaSTT(cfg *config.Config, capture *audio.Capture, vad *audio.VAD) (*SherpaSTT, error) {
	modelsDir := config.ModelsDir()
	model := cfg.WhisperModel

	whisperConfig := sherpa.OfflineWhisperModelConfig{
		Encoder:  filepath.Join(modelsDir, fmt.Sprintf("sherpa-onnx-whisper-%s-encoder.onnx", model)),
		Decoder:  filepath.Join(modelsDir, fmt.Sprintf("sherpa-onnx-whisper-%s-decoder.onnx", model)),
		Language: cfg.Language[:2], // "en-US" -> "en"
	}

	modelConfig := sherpa.OfflineModelConfig{
		Whisper: whisperConfig,
	}

	recognizerConfig := sherpa.OfflineRecognizerConfig{
		ModelConfig: modelConfig,
	}

	recognizer := sherpa.NewOfflineRecognizer(&recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create whisper recognizer (model: %s)", model)
	}

	return &SherpaSTT{
		cfg:        cfg,
		recognizer: recognizer,
		vad:        vad,
		capture:    capture,
	}, nil
}

// Transcribe listens for speech using VAD and transcribes with whisper.
func (s *SherpaSTT) Transcribe(ctx context.Context, onStatus func(phase string)) (string, error) {
	if onStatus != nil {
		onStatus("listening")
	}

	speechDetected := false
	timeout := time.After(time.Duration(s.cfg.ListenTimeout) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", nil // no speech detected
		default:
		}

		chunk := s.capture.Read()
		if chunk == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		s.vad.AcceptWaveform(chunk)

		if !speechDetected && s.vad.IsSpeech() {
			speechDetected = true
			if onStatus != nil {
				onStatus("hearing")
			}
		}

		if s.vad.IsSpeechDetected() {
			if onStatus != nil {
				onStatus("transcribing")
			}

			// Collect all speech segments
			var allSamples []float32
			for !s.vad.IsEmpty() {
				samples := s.vad.Front()
				allSamples = append(allSamples, samples...)
				s.vad.Pop()
			}

			// Transcribe
			stream := sherpa.NewOfflineStream(s.recognizer)
			stream.AcceptWaveform(audio.SampleRate, allSamples)
			s.recognizer.Decode(stream)
			result := stream.GetResult()
			sherpa.DeleteOfflineStream(stream)

			text := strings.TrimSpace(result.Text)
			if text != "" {
				return text, nil
			}

			// Empty transcription — keep listening
			speechDetected = false
			if onStatus != nil {
				onStatus("listening")
			}
		}
	}
}

// Available returns true if the STT provider is ready.
func (s *SherpaSTT) Available() bool {
	return s.recognizer != nil
}

// Delete frees resources.
func (s *SherpaSTT) Delete() {
	if s.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(s.recognizer)
	}
}
