package tts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

func TestQwenFakeWorker(t *testing.T) {
	if os.Getenv("SAMANTHA_QWEN_FAKE_WORKER") != "1" {
		return
	}

	if delay := os.Getenv("SAMANTHA_QWEN_FAKE_WORKER_SLEEP_MS"); delay != "" {
		ms := 0
		_, _ = fmt.Sscanf(delay, "%d", &ms)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if os.Getenv("SAMANTHA_QWEN_FAKE_WORKER_FAIL") == "1" {
		_, _ = os.Stderr.WriteString("deterministic fake worker failure\n")
		os.Exit(17)
	}

	output := os.Getenv("SAMANTHA_QWEN_FAKE_WORKER_OUTPUT")
	if output == "" {
		t.Fatal("fake worker output path is empty")
	}
	if os.Getenv("SAMANTHA_QWEN_FAKE_WORKER_INVALID") == "1" {
		if err := os.WriteFile(output, []byte("not a wav"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	if os.Getenv("SAMANTHA_QWEN_FAKE_WORKER_EMPTY") == "1" {
		if err := audio.WriteWAVFloat32(output, qwen3TTSSampleRate, nil); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	if err := audio.WriteWAVFloat32(output, qwen3TTSSampleRate, []float32{0.1, -0.2, 0.3}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestNewQwen3TTSValidation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{name: "nil config", cfg: nil, want: "nil config"},
		{name: "empty model", cfg: &config.Config{QwenTTSBinary: executable}, want: "qwen_tts_model is required"},
		{name: "missing binary", cfg: &config.Config{QwenTTSBinary: filepath.Join(t.TempDir(), "missing"), QwenTTSModel: t.TempDir()}, want: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewQwen3TTS(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewQwen3TTS() error = %v, want %q", err, tt.want)
			}
		})
	}

	nonDir := filepath.Join(t.TempDir(), "model-file")
	if err := os.WriteFile(nonDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewQwen3TTS(&config.Config{QwenTTSBinary: executable, QwenTTSModel: nonDir}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("NewQwen3TTS() non-directory error = %v, want not a directory", err)
	}

	q, err := NewQwen3TTS(&config.Config{QwenTTSBinary: executable, QwenTTSModel: t.TempDir()})
	if err != nil {
		t.Fatalf("NewQwen3TTS() valid config error = %v", err)
	}
	if !filepath.IsAbs(q.binary) {
		t.Fatalf("resolved binary = %q, want absolute path", q.binary)
	}
	if _, err := NewQwen3TTS(&config.Config{QwenTTSBinary: executable, QwenTTSModel: t.TempDir(), QwenTTSMode: "voicedesign"}); err == nil || !strings.Contains(err.Error(), "clear unsupported settings") {
		t.Fatalf("unsupported Qwen config error = %v, want actionable construction error", err)
	}
}

func TestQwenSynthesizeRequestRunsNativeWorkerAndStreamsWAV(t *testing.T) {
	model := t.TempDir()
	var gotArgs []string

	q := newQwen3TTS("fake-qwen3-tts", model, 5*time.Second, fakeQwenCommand(&gotArgs, ""))
	q.alive.Store(true)

	result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{
		Text:       "hello native worker",
		Voice:      "default",
		SampleRate: qwen3TTSSampleRate,
	})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}

	rate, err := result.Stream.WaitReady(context.Background())
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if rate != qwen3TTSSampleRate {
		t.Fatalf("sample rate = %d, want %d", rate, qwen3TTSSampleRate)
	}
	if result.Model != model || result.Provider != qwen3TTSProviderName {
		t.Fatalf("result identity = provider=%q model=%q, want provider=%q model=%q", result.Provider, result.Model, qwen3TTSProviderName, model)
	}
	var samples []float32
	for frame := range result.Stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := result.Stream.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
	wantSamples := []float32{0.1, -0.2, 0.3}
	if len(samples) != len(wantSamples) {
		t.Fatalf("samples = %v, want %v", samples, wantSamples)
	}
	for i := range wantSamples {
		if math.Abs(float64(samples[i]-wantSamples[i])) > 0.001 {
			t.Fatalf("samples = %v, want approximately %v", samples, wantSamples)
		}
	}
	if want := []string{"-m", model, "-t", "hello native worker"}; !reflect.DeepEqual(gotArgs[:4], want) {
		t.Fatalf("worker args = %v, want prefix %v", gotArgs, want)
	}
	if len(gotArgs) != 6 || gotArgs[4] != "-o" || filepath.Base(gotArgs[5]) != "speech.wav" {
		t.Fatalf("worker output args = %v, want -o <temp>/speech.wav", gotArgs)
	}
}

func TestQwenSynthesizeRequestRejectsUnsupportedControls(t *testing.T) {
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), time.Second, nil)
	q.alive.Store(true)

	tests := []struct {
		name string
		req  SynthesisRequest
		want string
	}{
		{name: "voice", req: SynthesisRequest{Text: "hi", Voice: "af_heart"}, want: "voice"},
		{name: "speed", req: SynthesisRequest{Text: "hi", Speed: 1.1}, want: "speech speed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := q.SynthesizeRequest(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SynthesizeRequest() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestQwenSynthesizeRequestPropagatesWorkerFailure(t *testing.T) {
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), time.Second, fakeQwenCommand(nil, "fail"))
	q.alive.Store(true)

	result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "fail"})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	for range result.Stream.Frames() {
	}
	if result.Stream.Err() == nil || !strings.Contains(result.Stream.Err().Error(), "deterministic fake worker failure") {
		t.Fatalf("stream error = %v, want worker stderr", result.Stream.Err())
	}
	if !IsProviderErrorKind(result.Stream.Err(), ProviderErrorWorker) || !errors.Is(result.Stream.Err(), ErrWorkerFailure) {
		t.Fatalf("stream error = %v, want worker failure classification", result.Stream.Err())
	}
}

func TestQwenSynthesizeRequestRejectsMalformedAndEmptyWAV(t *testing.T) {
	for _, mode := range []string{"invalid", "empty"} {
		t.Run(mode, func(t *testing.T) {
			q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), 5*time.Second, fakeQwenCommand(nil, mode))
			q.alive.Store(true)
			result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: mode})
			if err != nil {
				t.Fatalf("SynthesizeRequest() error = %v", err)
			}
			for range result.Stream.Frames() {
			}
			if err := result.Stream.Err(); err == nil || !IsProviderErrorKind(err, ProviderErrorMalformed) || !errors.Is(err, ErrMalformedOutput) {
				t.Fatalf("stream error = %v, want malformed-output classification", err)
			}
		})
	}
}

func TestQwenSynthesizeRequestTimesOutAndCancelsWorker(t *testing.T) {
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), 25*time.Millisecond, fakeQwenCommand(nil, "sleep"))
	q.alive.Store(true)

	result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "slow"})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	for range result.Stream.Frames() {
	}
	if err := result.Stream.Err(); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("stream error = %v, want timeout", err)
	}
}

func TestQwenSynthesizeRequestHonorsCallerCancellation(t *testing.T) {
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), time.Second, fakeQwenCommand(nil, "sleep"))
	q.alive.Store(true)
	ctx, cancel := context.WithCancel(context.Background())

	result, err := q.SynthesizeRequest(ctx, SynthesisRequest{Text: "cancel"})
	if err != nil {
		t.Fatalf("SynthesizeRequest() error = %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	cancel()
	for range result.Stream.Frames() {
	}
	if err := result.Stream.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", err)
	}
}

func TestQwenSynthesizeRequestRejectsUnsupportedSampleRate(t *testing.T) {
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), time.Second, nil)
	q.alive.Store(true)

	_, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", SampleRate: 16000})
	if err == nil || !strings.Contains(err.Error(), "cannot resample") {
		t.Fatalf("SynthesizeRequest() error = %v, want sample-rate rejection", err)
	}
}

func TestQwenSynthesizeRequestValidatesReferenceBeforeWorker(t *testing.T) {
	var gotArgs []string
	q := newQwen3TTS("fake-qwen3-tts", t.TempDir(), time.Second, fakeQwenCommand(&gotArgs, ""))
	q.alive.Store(true)

	_, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{
		Text:                "hello",
		Mode:                VoiceModeApprovedClone,
		ReferenceAudio:      filepath.Join(t.TempDir(), "missing.wav"),
		ReferenceTranscript: "hello",
	})
	if err == nil || !IsProviderErrorKind(err, ProviderErrorInput) || !strings.Contains(err.Error(), "voice mode") {
		t.Fatalf("unsupported reference mode error = %v, want actionable input error before reference decode", err)
	}
	if len(gotArgs) != 0 {
		t.Fatalf("worker args = %v, want no worker launch for invalid reference", gotArgs)
	}

	ref := filepath.Join(t.TempDir(), "reference.wav")
	if err := audio.WriteWAVFloat32(ref, qwen3TTSSampleRate, make([]float32, qwen3TTSSampleRate)); err != nil {
		t.Fatal(err)
	}
	_, err = q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hello", ReferenceAudio: ref})
	if err == nil || !strings.Contains(err.Error(), "reference voice") {
		t.Fatalf("unsupported reference error = %v, want early unsupported-control rejection", err)
	}
}

func fakeQwenCommand(gotArgs *[]string, mode string) qwenCommand {
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if gotArgs != nil {
			*gotArgs = append([]string(nil), args...)
		}
		output := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				output = args[i+1]
				break
			}
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQwenFakeWorker$")
		env := append([]string{}, os.Environ()...)
		env = append(env,
			"SAMANTHA_QWEN_FAKE_WORKER=1",
			"SAMANTHA_QWEN_FAKE_WORKER_OUTPUT="+output,
		)
		switch mode {
		case "fail":
			env = append(env, "SAMANTHA_QWEN_FAKE_WORKER_FAIL=1")
		case "sleep":
			env = append(env, "SAMANTHA_QWEN_FAKE_WORKER_SLEEP_MS=500")
		case "invalid":
			env = append(env, "SAMANTHA_QWEN_FAKE_WORKER_INVALID=1")
		case "empty":
			env = append(env, "SAMANTHA_QWEN_FAKE_WORKER_EMPTY=1")
		}
		cmd.Env = env
		return cmd
	}
}

var _ Provider = (*Qwen3TTS)(nil)
var _ RequestProvider = (*Qwen3TTS)(nil)
