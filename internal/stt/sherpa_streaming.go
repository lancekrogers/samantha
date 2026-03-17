package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/config"
)

// SherpaStreamingSTT implements streaming STT using sherpa-onnx online Zipformer.
type SherpaStreamingSTT struct {
	cfg        *config.Config
	recognizer *sherpa.OnlineRecognizer
	vad        *audio.VAD
	capture    audioSource
}

// NewSherpaStreamingSTT creates a new online sherpa-onnx STT provider.
func NewSherpaStreamingSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*SherpaStreamingSTT, error) {
	asset, err := config.SherpaStreamingModel(cfg.SherpaStreamingModel)
	if err != nil {
		return nil, err
	}

	modelDir := asset.ModelDir(config.ModelsDir())
	threads := min(runtime.NumCPU(), 4)

	recognizerConfig := sherpa.OnlineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{
			SampleRate: audio.SampleRate,
			FeatureDim: 80,
		},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: filepath.Join(modelDir, asset.EncoderFile(cfg.WhisperQuantized)),
				Decoder: filepath.Join(modelDir, asset.Decoder),
				Joiner:  filepath.Join(modelDir, asset.JoinerFile(cfg.WhisperQuantized)),
			},
			Tokens:     filepath.Join(modelDir, asset.Tokens),
			NumThreads: threads,
			Provider:   "cpu",
		},
		DecodingMethod:          "greedy_search",
		EnableEndpoint:          1,
		Rule1MinTrailingSilence: float32(max(cfg.VADSilenceDuration*2, 1.0)),
		Rule2MinTrailingSilence: float32(max(cfg.VADSilenceDuration, 0.45)),
		Rule3MinUtteranceLength: float32(max(cfg.PhraseTimeLimit, 5)),
		MaxActivePaths:          4,
	}

	recognizer := sherpa.NewOnlineRecognizer(&recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create streaming sherpa recognizer (model: %s)", cfg.SherpaStreamingModel)
	}

	return &SherpaStreamingSTT{
		cfg:        cfg,
		recognizer: recognizer,
		vad:        vad,
		capture:    capture,
	}, nil
}

// Start begins a streaming STT session with partial transcript support.
func (s *SherpaStreamingSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 16),
	}

	go s.runSession(sessionCtx, session.events)
	return session, nil
}

func (s *SherpaStreamingSTT) runSession(ctx context.Context, events chan<- Event) {
	defer close(events)

	s.vad.Clear()

	stream := sherpa.NewOnlineStream(s.recognizer)
	if stream == nil {
		events <- Failure{Err: fmt.Errorf("failed to create streaming sherpa stream")}
		return
	}
	defer func() {
		if stream != nil {
			sherpa.DeleteOnlineStream(stream)
		}
	}()

	lastPhaseAt := time.Now()
	emitPhase := func(phase string) {
		now := time.Now()
		events <- PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}
		lastPhaseAt = now
	}
	emitPhase("listening")

	listenDeadline := time.Now().Add(time.Duration(s.cfg.ListenTimeout) * time.Second)
	speechDetected := false
	transcribing := false
	speechStartedAt := time.Time{}
	lastPartial := ""

	for {
		select {
		case <-ctx.Done():
			events <- Failure{Err: ctx.Err()}
			return
		default:
		}

		if !speechDetected && time.Now().After(listenDeadline) {
			events <- Timeout{}
			return
		}

		chunk := s.capture.Read()
		if len(chunk) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		s.vad.AcceptWaveform(chunk)
		if !speechDetected && s.vad.IsSpeech() {
			speechDetected = true
			speechStartedAt = time.Now()
			emitPhase("hearing")
		}

		stream.AcceptWaveform(audio.SampleRate, chunk)
		for s.recognizer.IsReady(stream) {
			s.recognizer.Decode(stream)
		}

		partial := normalizeTranscript(strings.TrimSpace(s.recognizer.GetResult(stream).Text))
		if partial != "" {
			if !speechDetected {
				speechDetected = true
				speechStartedAt = time.Now()
				emitPhase("hearing")
			}
			if !transcribing {
				transcribing = true
				emitPhase("transcribing")
			}
			if partial != lastPartial {
				events <- PartialTranscript{Text: partial}
				lastPartial = partial
			}
		}

		phraseExpired := !speechStartedAt.IsZero() &&
			time.Since(speechStartedAt) >= time.Duration(max(s.cfg.PhraseTimeLimit, 1))*time.Second
		endpoint := speechDetected && (s.recognizer.IsEndpoint(stream) || s.vad.IsSpeechDetected() || phraseExpired)
		if !endpoint {
			continue
		}

		if !transcribing {
			transcribing = true
			emitPhase("transcribing")
		}

		finalText := normalizeTranscript(strings.TrimSpace(s.flushStream(stream)))
		if finalText != "" {
			events <- FinalTranscript{Text: finalText}
			return
		}

		// False positive or empty decode: reset state and keep listening.
		s.vad.Clear()
		speechDetected = false
		transcribing = false
		speechStartedAt = time.Time{}
		lastPartial = ""
		listenDeadline = time.Now().Add(time.Duration(s.cfg.ListenTimeout) * time.Second)

		sherpa.DeleteOnlineStream(stream)
		stream = sherpa.NewOnlineStream(s.recognizer)
		if stream == nil {
			events <- Failure{Err: fmt.Errorf("failed to reset streaming sherpa stream")}
			return
		}
		emitPhase("listening")
	}
}

func (s *SherpaStreamingSTT) flushStream(stream *sherpa.OnlineStream) string {
	stream.InputFinished()
	for s.recognizer.IsReady(stream) {
		s.recognizer.Decode(stream)
	}
	return s.recognizer.GetResult(stream).Text
}

// Available returns true if the STT provider is ready.
func (s *SherpaStreamingSTT) Available() bool {
	return s.recognizer != nil
}

// Delete frees resources.
func (s *SherpaStreamingSTT) Delete() {
	if s.recognizer != nil {
		sherpa.DeleteOnlineRecognizer(s.recognizer)
	}
}

func normalizeTranscript(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	if !looksMostlyUpper(text) {
		return text
	}
	return sentenceCase(strings.ToLower(text))
}

func looksMostlyUpper(text string) bool {
	letters := 0
	upper := 0
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	return letters > 0 && upper*100 >= letters*70
}

func sentenceCase(text string) string {
	var out []rune
	capNext := true

	for i, r := range []rune(text) {
		if capNext && unicode.IsLetter(r) {
			out = append(out, unicode.ToUpper(r))
			capNext = false
			continue
		}

		if r == 'i' && isWordBoundary(text, i-1) && isWordBoundary(text, i+1) {
			out = append(out, 'I')
			capNext = false
			continue
		}

		out = append(out, r)
		if r == '.' || r == '!' || r == '?' {
			capNext = true
		}
	}

	return string(out)
}

func isWordBoundary(text string, idx int) bool {
	runes := []rune(text)
	if idx < 0 || idx >= len(runes) {
		return true
	}
	return !unicode.IsLetter(runes[idx]) && !unicode.IsDigit(runes[idx])
}
