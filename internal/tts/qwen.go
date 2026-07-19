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
	defaultQwenTTSTimeout = 60 * time.Second
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
	binary  string
	model   string
	timeout time.Duration
	command qwenCommand
	alive   atomic.Bool
}

// NewQwen3TTS validates the configured native worker and model directory.
// qwen3-tts.cpp's current CLI accepts -m/--model, -t/--text, and -o/--output;
// this adapter intentionally depends only on that small file-based contract.
func NewQwen3TTS(cfg *config.Config) (*Qwen3TTS, error) {
	if cfg == nil {
		return nil, errors.New("qwen3-tts: nil config")
	}

	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	if binary == "" {
		binary = "qwen3-tts-cli"
	}
	model := strings.TrimSpace(cfg.QwenTTSModel)
	if model == "" {
		return nil, errors.New("qwen3-tts: qwen_tts_model is required")
	}
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
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

	q := newQwen3TTS(binary, model, timeout, exec.CommandContext)
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
	result, err := q.SynthesizeRequest(ctx, SynthesisRequest{Text: text})
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
		return SynthesisResult{}, errors.New("qwen3-tts: provider is closed")
	}
	if strings.TrimSpace(req.Text) == "" {
		return SynthesisResult{}, errors.New("qwen3-tts: text is empty")
	}
	if req.SampleRate != 0 && req.SampleRate != qwen3TTSSampleRate {
		return SynthesisResult{}, fmt.Errorf("qwen3-tts cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, qwen3TTSSampleRate)
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
		Voice:      voice,
	}, nil
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

	args := []string{
		"-m", q.model,
		"-t", req.Text,
		"-o", outputPath,
	}
	cmd := q.command(runCtx, q.binary, args...)
	var stdout, stderr limitedBuffer
	stdout.limit = maxWorkerOutput
	stderr.limit = maxWorkerOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			stream.CloseWithError(ctx.Err())
			return
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			stream.CloseWithError(fmt.Errorf("qwen3-tts: worker timed out after %s", q.timeout))
			return
		}
		stream.CloseWithError(fmt.Errorf("qwen3-tts: worker failed: %w%s", err, workerOutputSuffix(stderr.String(), stdout.String())))
		return
	}
	if err := runCtx.Err(); err != nil {
		stream.CloseWithError(err)
		return
	}

	samples, sampleRate, err := audio.ReadWAVFloat32(outputPath)
	if err != nil {
		stream.CloseWithError(fmt.Errorf("qwen3-tts: read worker WAV: %w", err))
		return
	}
	if sampleRate != qwen3TTSSampleRate {
		stream.CloseWithError(fmt.Errorf("qwen3-tts: worker returned %d Hz, want %d Hz", sampleRate, qwen3TTSSampleRate))
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

// Delete prevents new work. Existing workers are still bounded by their
// request context; callers should cancel that context during shutdown.
func (q *Qwen3TTS) Delete() { q.alive.Store(false) }
