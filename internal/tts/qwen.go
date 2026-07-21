package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

const (
	qwen3TTSProviderName  = "qwen3-tts"
	qwen3TTSSampleRate    = 24000
	defaultQwenTTSTimeout = 120 * time.Second
	maxQwenAudioDuration  = 2 * time.Hour
	maxWorkerOutput       = 8 << 10
)

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return written, nil
}

// qwenCommand starts one bounded native synthesis process. Keeping this as a
// seam makes the provider testable without downloading or vendoring models.
type qwenCommand func(context.Context, string, ...string) *exec.Cmd

// Qwen3TTS implements the optional Qwen3-TTS provider through an externally
// installed qwen3-tts.cpp-compatible CLI. Samantha owns the process lifetime;
// model files and the native executable remain outside this repository.
type Qwen3TTS struct {
	binary              string
	model               string
	timeout             time.Duration
	command             qwenCommand
	alive               atomic.Bool
	mode                VoiceMode
	voice               string
	language            string
	instruction         string
	referenceAudio      string
	referenceTranscript string
	consent             bool
}

// NewQwen3TTS validates the configured native worker and model directory.
// qwen3-tts.cpp's current CLI accepts -m/--model, -t/--text, and -o/--output;
// this adapter intentionally depends only on that small file-based contract.
func NewQwen3TTS(cfg *config.Config) (*Qwen3TTS, error) {
	if cfg == nil {
		return nil, errors.New("qwen3-tts: nil config")
	}
	if err := config.ValidateQwenTTSConfig(cfg); err != nil {
		return nil, fmt.Errorf("qwen3-tts: %w", err)
	}

	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	if binary == "" {
		binary = "qwen3-tts-cli"
	}
	model := strings.TrimSpace(cfg.QwenTTSModel)
	if model == "" {
		return nil, errors.New("qwen3-tts: qwen_tts_model is required")
	}
	binaryPath, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath, err = filepath.Abs(binaryPath)
		if err != nil {
			return nil, fmt.Errorf("qwen3-tts: resolve native worker %q: %w", binary, err)
		}
	}
	modelInfo, err := os.Stat(model)
	if err != nil {
		return nil, fmt.Errorf("qwen3-tts: model directory %q: %w", model, err)
	}
	if !modelInfo.IsDir() {
		return nil, fmt.Errorf("qwen3-tts: model path %q is not a directory", model)
	}

	timeout := defaultQwenTTSTimeout
	if cfg.QwenTTSTimeout > 0 {
		timeout = time.Duration(cfg.QwenTTSTimeout) * time.Second
	}

	q := newQwen3TTS(binaryPath, model, timeout, exec.CommandContext)
	q.mode = VoiceMode(strings.TrimSpace(cfg.QwenTTSMode))
	q.voice = strings.TrimSpace(cfg.QwenTTSVoice)
	q.language = strings.TrimSpace(cfg.QwenTTSLanguage)
	q.instruction = cfg.QwenTTSInstruction
	q.referenceAudio = strings.TrimSpace(cfg.QwenTTSReferenceAudio)
	q.referenceTranscript = cfg.QwenTTSReferenceText
	q.consent = cfg.QwenTTSConsent
	q.alive.Store(true)
	return q, nil
}

func newQwen3TTS(binary, model string, timeout time.Duration, command qwenCommand) *Qwen3TTS {
	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	if command == nil {
		command = exec.CommandContext
	}
	return &Qwen3TTS{binary: binary, model: model, timeout: timeout, command: command}
}

// Synthesize streams synthesized PCM frames for the given text.
func (q *Qwen3TTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := q.SynthesizeRequest(ctx, SynthesisRequest{
		Text:                text,
		Voice:               q.voice,
		Mode:                q.mode,
		Language:            q.language,
		Instruction:         q.instruction,
		ReferenceAudio:      q.referenceAudio,
		ReferenceTranscript: q.referenceTranscript,
	})
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

// SynthesizeRequest starts a native worker and converts its 24 kHz WAV output
// into Samantha's existing float32 PCM stream. The worker is intentionally
// per-request for this spike: cancellation and timeout then have an explicit
// process boundary, while a future persistent worker can reuse this seam.
func (q *Qwen3TTS) SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !q.Available() {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if strings.TrimSpace(req.Text) == "" {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorInput, Err: errors.New("text is empty")}
	}
	if req.SampleRate != 0 && req.SampleRate != qwen3TTSSampleRate {
		return SynthesisResult{}, qwenUnsupported("sample rate", fmt.Sprintf("cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, qwen3TTSSampleRate))
	}
	if voice := strings.TrimSpace(req.Voice); voice != "" && !strings.EqualFold(voice, "default") {
		return SynthesisResult{}, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the native CLI; use the model's default voice", req.Voice))
	}
	if req.Speed != 0 {
		return SynthesisResult{}, qwenUnsupported("speech speed", "unsupported by the native CLI; omit speed")
	}
	if req.Mode != "" && req.Mode != VoiceModeStatic {
		return SynthesisResult{}, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the native CLI", req.Mode))
	}
	if strings.TrimSpace(req.Language) != "" {
		return SynthesisResult{}, qwenUnsupported("language", "language selection is not exposed by the native CLI")
	}
	if strings.TrimSpace(req.Instruction) != "" {
		return SynthesisResult{}, qwenUnsupported("voice instruction", "voice design is not exposed by the native CLI")
	}
	if strings.TrimSpace(req.ReferenceAudio) != "" || strings.TrimSpace(req.ReferenceTranscript) != "" {
		return SynthesisResult{}, qwenUnsupported("reference voice", "reference-audio cloning is not exposed by the native CLI")
	}
	if err := validateQwenReference(req); err != nil {
		return SynthesisResult{}, err
	}
	if req.Mode == VoiceModeApprovedClone && !q.consent {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate request", Kind: ProviderErrorInput, Err: errors.New("explicit consent is required for approved voice cloning")}
	}

	voice := req.Voice
	if voice == "" {
		voice = "default"
	}
	stream := audio.NewPCMStream(ctx)
	go q.synthesize(ctx, req, stream)

	return SynthesisResult{
		Stream:     stream,
		SampleRate: qwen3TTSSampleRate,
		Provider:   qwen3TTSProviderName,
		Model:      q.model,
		Voice:      voice,
		Mode:       VoiceModeStatic,
	}, nil
}

func qwenUnsupported(feature, detail string) error {
	return &ProviderError{
		Provider:  qwen3TTSProviderName,
		Operation: "validate request",
		Kind:      ProviderErrorInput,
		Err:       &UnsupportedFeatureError{Provider: qwen3TTSProviderName, Feature: feature, Detail: detail},
	}
}

func (q *Qwen3TTS) synthesize(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) {
	tmpDir, err := os.MkdirTemp("", "samantha-qwen3-tts-")
	if err != nil {
		stream.CloseWithError(fmt.Errorf("qwen3-tts: create output directory: %w", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "speech.wav")
	runCtx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()

	args, err := q.buildArgs(req, outputPath)
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	cmd := q.command(runCtx, q.binary, args...)
	if cmd == nil {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "start worker", Kind: ProviderErrorWorker, Err: errors.New("command factory returned nil")})
		return
	}
	configureQwenCommand(cmd)
	var stdout, stderr limitedBuffer
	stdout.limit = maxWorkerOutput
	stderr.limit = maxWorkerOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "cancel worker", Kind: ProviderErrorCanceled, Err: ctx.Err()})
			return
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: ProviderErrorWorker, Err: fmt.Errorf("worker timed out after %s", q.timeout)})
			return
		}
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: ProviderErrorWorker, Err: fmt.Errorf("worker failed: %w%s", err, workerOutputSuffix(stderr.String(), stdout.String()))})
		return
	}
	if err := runCtx.Err(); err != nil {
		kind := ProviderErrorWorker
		if errors.Is(err, context.Canceled) {
			kind = ProviderErrorCanceled
		}
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: kind, Err: err})
		return
	}

	samples, sampleRate, err := audio.ReadWAVFloat32(outputPath)
	if err != nil {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "read worker WAV", Kind: ProviderErrorMalformed, Err: err})
		return
	}
	if len(samples) == 0 {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: errors.New("worker returned empty audio")})
		return
	}
	if sampleRate != qwen3TTSSampleRate {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: fmt.Errorf("worker returned %d Hz, want %d Hz", sampleRate, qwen3TTSSampleRate)})
		return
	}
	if duration := time.Duration(float64(len(samples)) / float64(sampleRate) * float64(time.Second)); duration > maxQwenAudioDuration {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: fmt.Errorf("worker returned %s of audio, maximum is %s", duration.Round(time.Second), maxQwenAudioDuration)})
		return
	}
	if err := stream.SetSampleRate(sampleRate); err != nil {
		stream.CloseWithError(err)
		return
	}

	const chunkSize = 2048
	for start := 0; start < len(samples); start += chunkSize {
		if err := ctx.Err(); err != nil {
			stream.CloseWithError(err)
			return
		}
		end := min(start+chunkSize, len(samples))
		if err := stream.Write(samples[start:end]); err != nil {
			stream.CloseWithError(err)
			return
		}
	}
	stream.Close()
}

// buildArgs is the only place where provider input becomes a native worker
// argument vector. The currently verified Samantha worker supports only the
// baseline model/text/output contract; future probed modes must extend this
// function with explicit flags and corresponding capability tests.
func (q *Qwen3TTS) buildArgs(req SynthesisRequest, outputPath string) ([]string, error) {
	if req.Mode != "" && req.Mode != VoiceModeStatic {
		return nil, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the native CLI", req.Mode))
	}
	if strings.TrimSpace(req.Voice) != "" && !strings.EqualFold(req.Voice, "default") {
		return nil, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the native CLI", req.Voice))
	}
	if req.Language != "" || req.Instruction != "" || req.ReferenceAudio != "" || req.ReferenceTranscript != "" {
		return nil, qwenUnsupported("voice controls", "the verified native CLI exposes no voice-mode flags")
	}
	return []string{"-m", q.model, "-t", req.Text, "-o", outputPath}, nil
}

func validateQwenReference(req SynthesisRequest) error {
	path := strings.TrimSpace(req.ReferenceAudio)
	if path == "" {
		if strings.TrimSpace(req.ReferenceTranscript) != "" {
			return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference transcript requires reference audio")}
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: fmt.Errorf("reference audio is unavailable")}
	}
	if !info.Mode().IsRegular() {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio must be a regular file")}
	}
	if info.Size() > 50<<20 {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio exceeds the 50 MiB limit")}
	}
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil || len(samples) == 0 || rate <= 0 {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio must be a non-empty readable WAV")}
	}
	duration := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))
	if duration < time.Second || duration > 30*time.Second {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio duration must be between 1 and 30 seconds")}
	}
	if strings.TrimSpace(req.ReferenceTranscript) == "" {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference transcript is required")}
	}
	return nil
}

func workerOutputSuffix(stderr, stdout string) string {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return ": " + detail
	}
	if detail := strings.TrimSpace(stdout); detail != "" {
		return ": " + detail
	}
	return ""
}

// Available returns true while this provider may start native workers.
func (q *Qwen3TTS) Available() bool { return q.alive.Load() }

// ListVoices returns no static list because Qwen voice capabilities depend on
// the selected model and optional reference-audio workflow.
func (q *Qwen3TTS) ListVoices(locale, gender string) []Voice { return nil }

// Capabilities reports only the baseline native worker guarantees. Voice
// modes are intentionally empty until a worker/model probe verifies them.
func (q *Qwen3TTS) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider:               qwen3TTSProviderName,
		Model:                  q.model,
		ModelReady:             q.model != "" && q.Available(),
		SampleRates:            []int{qwen3TTSSampleRate},
		SupportsCancellation:   true,
		SupportsReferenceAudio: false,
		SupportsSpeed:          false,
	}
}

func (q *Qwen3TTS) Status() ProviderStatus {
	available := q.Available()
	detail := "native worker and model configured"
	if !available {
		detail = "provider is closed"
	}
	return ProviderStatus{Provider: qwen3TTSProviderName, Available: available, ModelReady: q.model != "" && available, Detail: detail}
}

// Delete prevents new work. Existing workers are still bounded by their
// request context; callers should cancel that context during shutdown.
func (q *Qwen3TTS) Delete() { q.alive.Store(false) }
