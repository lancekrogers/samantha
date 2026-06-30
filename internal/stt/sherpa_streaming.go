package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/endpoint"
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

// Start begins a streaming STT session with partial transcript support over the
// typed frame contract and the shared endpoint policy.
func (s *SherpaStreamingSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 16),
	}

	finite := sourceKind(s.capture) != audio.SourceLive
	policy := endpoint.FromConfig(s.cfg, finite)
	policy.AllowProviderEnd = true // the streaming recognizer self-endpoints

	go func() {
		defer close(session.events)

		stream := sherpa.NewOnlineStream(s.recognizer)
		if stream == nil {
			session.events <- Failure{Err: fmt.Errorf("failed to create streaming sherpa stream")}
			return
		}
		rec := &onlineRec{recognizer: s.recognizer, stream: stream}
		defer rec.close()

		runStreamingLoop(sessionCtx, streamingLoopDeps{
			frames: asFrameSource(s.capture),
			seg:    vadSegmenter{vad: s.vad},
			rec:    rec,
			policy: policy,
		}, session.events)
	}()
	return session, nil
}

// onlineRec adapts the cgo sherpa online recognizer to the streamingRecognizer
// seam so the streaming loop can run against either the real recognizer or a fake.
type onlineRec struct {
	recognizer *sherpa.OnlineRecognizer
	stream     *sherpa.OnlineStream
}

func (o *onlineRec) Accept(samples []float32) {
	o.stream.AcceptWaveform(audio.SampleRate, samples)
	for o.recognizer.IsReady(o.stream) {
		o.recognizer.Decode(o.stream)
	}
}

func (o *onlineRec) Partial() string  { return o.recognizer.GetResult(o.stream).Text }
func (o *onlineRec) IsEndpoint() bool { return o.recognizer.IsEndpoint(o.stream) }

func (o *onlineRec) Finalize() string {
	o.stream.InputFinished()
	for o.recognizer.IsReady(o.stream) {
		o.recognizer.Decode(o.stream)
	}
	return o.recognizer.GetResult(o.stream).Text
}

func (o *onlineRec) Reset() error {
	stream := sherpa.NewOnlineStream(o.recognizer)
	if stream == nil {
		return fmt.Errorf("failed to create streaming sherpa stream")
	}
	sherpa.DeleteOnlineStream(o.stream)
	o.stream = stream
	return nil
}

func (o *onlineRec) close() {
	if o.stream != nil {
		sherpa.DeleteOnlineStream(o.stream)
		o.stream = nil
	}
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
