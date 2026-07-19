package tts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
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
	if err := audio.WriteWAVFloat32(output, qwen3TTSSampleRate, []float32{0.1, -0.2, 0.3}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestQwenSynthesizeRequestRunsNativeWorkerAndStreamsWAV(t *testing.T) {
	model := t.TempDir()
	var gotArgs []string

	q := newQwen3TTS("fake-qwen3-tts", model, 5*time.Second, fakeQwenCommand(&gotArgs, ""))
	q.alive.Store(true)

	result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{
		Text:       "hello native worker",
		Voice:      "qwen-default",
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
	if len(gotArgs) != 6 || gotArgs[4] != "-o" || !strings.HasSuffix(gotArgs[5], "/speech.wav") {
		t.Fatalf("worker output args = %v, want -o <temp>/speech.wav", gotArgs)
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
		}
		cmd.Env = env
		return cmd
	}
}

var _ Provider = (*Qwen3TTS)(nil)
var _ RequestProvider = (*Qwen3TTS)(nil)
