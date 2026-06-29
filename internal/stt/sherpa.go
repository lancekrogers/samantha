package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

// SherpaOfflineSTT implements utterance-final STT using sherpa-onnx whisper.
type SherpaOfflineSTT struct {
	cfg        *config.Config
	recognizer *sherpa.OfflineRecognizer
	vad        *audio.VAD
	capture    audioSource
}

type sherpaSession struct {
	cancel context.CancelFunc
	events chan Event
}

func (s *sherpaSession) Events() <-chan Event {
	return s.events
}

func (s *sherpaSession) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// NewSherpaOfflineSTT creates a new sherpa-onnx whisper STT provider.
func NewSherpaOfflineSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*SherpaOfflineSTT, error) {
	modelsDir := config.ModelsDir()
	model := cfg.WhisperModel

	suffix := ".onnx"
	if cfg.WhisperQuantized {
		suffix = ".int8.onnx"
	}

	whisperConfig := sherpa.OfflineWhisperModelConfig{
		Encoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-encoder%s", model, suffix)),
		Decoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-decoder%s", model, suffix)),
		Language: cfg.Language[:2], // "en-US" -> "en"
	}

	threads := min(runtime.NumCPU(), 4)
	modelConfig := sherpa.OfflineModelConfig{
		Whisper:    whisperConfig,
		Tokens:     filepath.Join(modelsDir, fmt.Sprintf("%s-tokens.txt", model)),
		NumThreads: threads,
	}

	recognizerConfig := sherpa.OfflineRecognizerConfig{
		ModelConfig: modelConfig,
	}

	recognizer := sherpa.NewOfflineRecognizer(&recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create whisper recognizer (model: %s)", model)
	}

	return &SherpaOfflineSTT{
		cfg:        cfg,
		recognizer: recognizer,
		vad:        vad,
		capture:    capture,
	}, nil
}

// minSpeechSamples is the minimum number of audio samples for valid speech (0.3s at 16kHz).
const minSpeechSamples = 4800

// Start begins an STT session using utterance-final decoding.
func (s *SherpaOfflineSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 8),
	}

	go s.runSession(sessionCtx, session.events)
	return session, nil
}

func (s *SherpaOfflineSTT) runSession(ctx context.Context, events chan<- Event) {
	defer close(events)

	// Flush stale VAD segments from previous turn.
	// Do NOT reset capture — the user may already be speaking.
	s.vad.Clear()
	lastPhaseAt := time.Now()
	emitPhase := func(phase string) {
		now := time.Now()
		events <- PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}
		lastPhaseAt = now
	}
	emitPhase("listening")

	speechDetected := false
	timeout := time.After(time.Duration(s.cfg.ListenTimeout) * time.Second)

	for {
		select {
		case <-ctx.Done():
			events <- Failure{Err: ctx.Err()}
			return
		case <-timeout:
			events <- Timeout{}
			return
		default:
		}

		chunk := s.capture.Read()
		exhausted := len(chunk) == 0 && sourceExhausted(s.capture)
		if len(chunk) == 0 {
			if !exhausted {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			s.vad.Flush()
			if !speechDetected && !s.vad.IsSpeechDetected() {
				events <- Timeout{}
				return
			}
		} else {
			s.vad.AcceptWaveform(chunk)

			if !speechDetected && s.vad.IsSpeech() {
				speechDetected = true
				emitPhase("hearing")
			}
		}

		if s.vad.IsSpeechDetected() {
			emitPhase("transcribing")

			// Collect all speech segments
			var allSamples []float32
			for !s.vad.IsEmpty() {
				samples := s.vad.Front()
				allSamples = append(allSamples, samples...)
				s.vad.Pop()
			}

			// Skip if too short to be real speech.
			if len(allSamples) < minSpeechSamples {
				if exhausted {
					events <- Timeout{}
					return
				}
				speechDetected = false
				emitPhase("listening")
				continue
			}

			// Transcribe
			stream := sherpa.NewOfflineStream(s.recognizer)
			stream.AcceptWaveform(audio.SampleRate, allSamples)
			s.recognizer.Decode(stream)
			result := stream.GetResult()
			sherpa.DeleteOfflineStream(stream)

			text := strings.TrimSpace(result.Text)
			if text != "" {
				events <- FinalTranscript{Text: text}
				return
			}

			// Empty transcription — keep listening
			if exhausted {
				events <- Timeout{}
				return
			}
			speechDetected = false
			emitPhase("listening")
		}

		if exhausted {
			events <- Timeout{}
			return
		}
	}
}

// Available returns true if the STT provider is ready.
func (s *SherpaOfflineSTT) Available() bool {
	return s.recognizer != nil
}

// Delete frees resources.
func (s *SherpaOfflineSTT) Delete() {
	if s.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(s.recognizer)
	}
}
