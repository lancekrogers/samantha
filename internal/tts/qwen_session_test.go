package tts

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
)

func TestManagedQwenSessionHelper(t *testing.T) {
	if os.Getenv("SAMANTHA_QWEN_FAKE_SESSION") != "1" {
		return
	}
	fmt.Println(`{"protocol":"samantha-qwen/v1","type":"ready","voices":["Vivian","Ryan"],"languages":["Auto","English"]}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg managedQwenMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			os.Exit(2)
		}
		if msg.Type == "shutdown" {
			return
		}
		marker := os.Getenv("SAMANTHA_QWEN_FAKE_SESSION_MARKER")
		if marker != "" {
			if _, err := os.Stat(marker); os.IsNotExist(err) {
				if err := os.WriteFile(marker, []byte("attempted"), 0o600); err != nil {
					os.Exit(3)
				}
				switch os.Getenv("SAMANTHA_QWEN_FAKE_SESSION_MODE") {
				case "crash-once":
					os.Exit(17)
				case "block-once":
					time.Sleep(5 * time.Second)
				}
			}
		}
		// Emit progressive PCM like the real worker (post-generation).
		samples := []float32{0.1, -0.1, 0.2}
		raw := make([]byte, len(samples)*4)
		for i, s := range samples {
			u := math.Float32bits(s)
			raw[i*4] = byte(u)
			raw[i*4+1] = byte(u >> 8)
			raw[i*4+2] = byte(u >> 16)
			raw[i*4+3] = byte(u >> 24)
		}
		chunk := managedQwenMessage{
			Protocol: managedQwenProtocol, Type: "audio_chunk", RequestID: msg.RequestID,
			SampleRate: qwen3TTSSampleRate, PCMf32leB64: base64.StdEncoding.EncodeToString(raw),
		}
		data, _ := json.Marshal(chunk)
		fmt.Println(string(data))
		if msg.OutputPath != "" {
			if err := audio.WriteWAVFloat32(msg.OutputPath, qwen3TTSSampleRate, samples); err != nil {
				os.Exit(4)
			}
		}
		response := managedQwenMessage{
			Protocol: managedQwenProtocol, Type: "complete", RequestID: msg.RequestID,
			SampleRate: qwen3TTSSampleRate,
		}
		data, _ = json.Marshal(response)
		fmt.Println(string(data))
	}
}

func TestManagedQwenSessionHandshakeAndSynthesis(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-worker.sh")
	source := `#!/bin/sh
echo 'optional dependency notice'
echo ''
echo '{"protocol":"samantha-qwen/v1","type":"ready","voices":["Vivian"]}'
while IFS= read -r request; do
  case "$request" in
    *'"type":"shutdown"'*) exit 0 ;;
    *) echo 'model progress'; echo '{"protocol":"samantha-qwen/v1","type":"complete","request_id":"qwen-1","sample_rate":24000}' ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := startManagedQwenSession("sh", script, "/models/qwen", time.Second)
	if err != nil {
		t.Fatalf("startManagedQwenSession() error = %v", err)
	}
	defer session.Close()
	// Legacy file path still works when worker only sends complete (no chunks).
	if err := session.Synthesize(context.Background(), SynthesisRequest{
		Text: "hello", Mode: VoiceModeCustomVoice, Voice: "Vivian", Language: "English",
	}, filepath.Join(t.TempDir(), "speech.wav")); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
}

func TestManagedQwenSessionStreamsPCMChunks(t *testing.T) {
	script := filepath.Join(t.TempDir(), "pcm-worker.sh")
	// Three float32 samples (0.5, -0.5, 0.25) encoded as little-endian base64 in
	// Go and injected into the worker script below.
	samples := []float32{0.5, -0.5, 0.25}
	raw := make([]byte, len(samples)*4)
	for i, s := range samples {
		u := math.Float32bits(s)
		raw[i*4] = byte(u)
		raw[i*4+1] = byte(u >> 8)
		raw[i*4+2] = byte(u >> 16)
		raw[i*4+3] = byte(u >> 24)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	source := fmt.Sprintf(`#!/bin/sh
echo '{"protocol":"samantha-qwen/v1","type":"ready","voices":["Vivian"]}'
while IFS= read -r request; do
  case "$request" in
    *'"type":"shutdown"'*) exit 0 ;;
    *)
      echo '{"protocol":"samantha-qwen/v1","type":"audio_chunk","request_id":"qwen-1","sample_rate":24000,"pcm_f32le_b64":"%s"}'
      echo '{"protocol":"samantha-qwen/v1","type":"complete","request_id":"qwen-1","sample_rate":24000}'
      ;;
  esac
done
`, b64)
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := startManagedQwenSession("sh", script, "/models/qwen", time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer session.Close()
	stream := audio.NewPCMStream(context.Background())
	if err := session.SynthesizeToStream(context.Background(), SynthesisRequest{
		Text: "hello", Mode: VoiceModeCustomVoice, Voice: "Vivian", Language: "English",
	}, stream); err != nil {
		t.Fatalf("SynthesizeToStream: %v", err)
	}
	stream.Close()
	got := drainStream(t, stream)
	if len(got) != 3 {
		t.Fatalf("samples = %v, want 3", got)
	}
	for i, want := range samples {
		if math.Abs(float64(got[i]-want)) > 1e-6 {
			t.Fatalf("sample[%d] = %v, want %v", i, got[i], want)
		}
	}
}

func TestManagedQwenSessionRejectsBadHandshake(t *testing.T) {
	script := filepath.Join(t.TempDir(), "bad-worker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho nope\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := startManagedQwenSession("sh", script, "/models/qwen", time.Second); err == nil {
		t.Fatal("bad managed worker handshake was accepted")
	}
}

func TestManagedQwenSessionStartupHonorsCancellation(t *testing.T) {
	script := filepath.Join(t.TempDir(), "slow-worker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := startManagedQwenSessionContext(ctx, "sh", script, "/models/qwen", time.Minute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled startup took %s", elapsed)
	}
}

func TestManagedQwenProviderRestartsAndRetriesAfterWorkerCrash(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-attempt")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION", "1")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION_MODE", "crash-once")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION_MARKER", marker)

	q := newManagedSessionTestProvider(t)
	defer q.Delete()
	stream, err := q.Synthesize(context.Background(), "recover after a crash")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if samples := drainStream(t, stream); len(samples) == 0 {
		t.Fatal("restarted worker returned no samples")
	}
	if !q.Available() {
		t.Fatal("provider became unavailable after a recovered worker crash")
	}
}

func TestManagedQwenProviderRecoversOnRequestAfterCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-attempt")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION", "1")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION_MODE", "block-once")
	t.Setenv("SAMANTHA_QWEN_FAKE_SESSION_MARKER", marker)

	q := newManagedSessionTestProvider(t)
	defer q.Delete()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := q.Synthesize(ctx, "cancel this request")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake worker never received the cancellable request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	for range stream.Frames() {
	}
	if err := stream.Err(); err == nil || !IsProviderErrorKind(err, ProviderErrorCanceled) {
		t.Fatalf("canceled stream error = %v, want canceled provider error", err)
	}
	if !q.Available() {
		t.Fatal("cancellation permanently closed the provider")
	}

	recovered, err := q.Synthesize(context.Background(), "the next request works")
	if err != nil {
		t.Fatalf("recovery Synthesize() error = %v", err)
	}
	if samples := drainStream(t, recovered); len(samples) == 0 {
		t.Fatal("replacement worker returned no samples")
	}
}

func newManagedSessionTestProvider(t *testing.T) *Qwen3TTS {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	q := newQwen3TTS(executable, t.TempDir(), 2*time.Second, nil)
	q.managed = true
	q.workerScript = "-test.run=^TestManagedQwenSessionHelper$"
	q.startupTimeout = 2 * time.Second
	q.mode = VoiceModeCustomVoice
	q.voice = "Vivian"
	q.language = "Auto"
	q.alive.Store(true)
	session, err := startManagedQwenSession(q.binary, q.workerScript, q.model, q.startupTimeout)
	if err != nil {
		t.Fatalf("start fake managed session: %v", err)
	}
	q.session = session
	return q
}
