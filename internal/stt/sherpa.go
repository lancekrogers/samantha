package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/endpoint"
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
	model, err := config.SherpaOfflineWhisperModel(cfg.WhisperModel)
	if err != nil {
		return nil, err
	}

	suffix := ".onnx"
	if cfg.WhisperQuantized {
		suffix = ".int8.onnx"
	}

	whisperConfig := sherpa.OfflineWhisperModelConfig{
		Encoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-encoder%s", model, suffix)),
		Decoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-decoder%s", model, suffix)),
		Language: config.LanguageCode(cfg.Language), // "en-US" -> "en"
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

// Start begins an STT session using utterance-final decoding over the typed
// frame contract and the endpoint policy.
func (s *SherpaOfflineSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 8),
	}

	finite := sourceKind(s.capture) != audio.SourceLive
	deps := offlineLoopDeps{
		frames:     asFrameSource(s.capture),
		seg:        vadSegmenter{vad: s.vad},
		policy:     endpoint.FromConfig(s.cfg, finite),
		transcribe: s.transcribe,
	}

	go func() {
		defer close(session.events)
		runOfflineLoop(sessionCtx, deps, session.events)
	}()
	return session, nil
}

// transcribe decodes finalized speech samples to text via the cgo sherpa
// recognizer. It is the production transcribeFunc seam for runOfflineLoop.
func (s *SherpaOfflineSTT) transcribe(samples []float32) (string, error) {
	stream := sherpa.NewOfflineStream(s.recognizer)
	if stream == nil {
		return "", fmt.Errorf("failed to create whisper stream")
	}
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(audio.SampleRate, samples)
	s.recognizer.Decode(stream)
	return normalizeTranscript(strings.TrimSpace(stream.GetResult().Text)), nil
}

// vadSegmenter adapts the cgo Silero *audio.VAD to the segmenter seam so the
// offline loop can run against either the real VAD or a deterministic fake.
type vadSegmenter struct{ vad *audio.VAD }

func (s vadSegmenter) AcceptWaveform(samples []float32) { s.vad.AcceptWaveform(samples) }
func (s vadSegmenter) IsSpeech() bool                   { return s.vad.IsSpeech() }
func (s vadSegmenter) HasSegments() bool                { return s.vad.IsSpeechDetected() }

func (s vadSegmenter) NextSegment() ([]float32, bool) {
	if s.vad.IsEmpty() {
		return nil, false
	}
	seg := s.vad.Front()
	s.vad.Pop()
	return seg, true
}

func (s vadSegmenter) Reset() { s.vad.Clear() }
func (s vadSegmenter) Flush() { s.vad.Flush() }

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
