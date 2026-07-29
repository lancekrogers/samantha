package tts

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

func TestIsNativeWorkerBinary(t *testing.T) {
	if !isNativeWorkerBinary("/opt/bin/qwen3-tts-worker") {
		t.Fatal("expected worker binary match")
	}
	if isNativeWorkerBinary("qwen3-tts-cli") {
		t.Fatal("cli must not match worker")
	}
}

func TestFindNativeInstall(t *testing.T) {
	root := t.TempDir()
	modelsDir := root
	install := filepath.Join(modelsDir, "qwen3-tts")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(install, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(install, "bin", "qwen3-tts-worker")
	if err := os.WriteFile(worker, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "models", "qwen3-tts-0.6b-f16.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := findNativeInstall(modelsDir)
	if !ok {
		t.Fatal("expected native install")
	}
	if got.Worker != worker {
		t.Fatalf("worker=%q want %q", got.Worker, worker)
	}
}

func TestNativeQwenSessionHandshakeAndPCM(t *testing.T) {
	// Fake worker: ready + pcm_meta + 3 f32 samples + final.
	samples := []float32{0.5, -0.5, 0.25}
	raw := make([]byte, len(samples)*4)
	for i, s := range samples {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(s))
	}
	script := filepath.Join(t.TempDir(), "fake-native-worker.sh")
	// Use printf for binary after JSON line. Write raw via python for portability.
	source := `#!/usr/bin/env bash
set -euo pipefail
echo '{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian"]}'
while IFS= read -r line; do
  case "$line" in
    *'"shutdown"'*) exit 0 ;;
    *'"cancel"'*) continue ;;
    *'"synthesize"'*)
      echo '{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":3}'
      # three float32: 0.5, -0.5, 0.25 little-endian
      printf '\%s' '\x00\x00\x00\x3f\x00\x00\x00\xbf\x00\x00\x80\x3e'
      printf '\n'
      echo '{"type":"final","id":"nqwen-1"}'
      ;;
  esac
done
`
	// Fix binary printf - use a cleaner approach with a small go helper file instead
	_ = source
	// Write a Go-based fake via executable of this test when env set is heavy;
	// use shell with base64-decoded PCM instead.
	b64src := `#!/usr/bin/env bash
set -euo pipefail
echo '{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian"]}'
while IFS= read -r line; do
  case "$line" in
    *'"shutdown"'*) exit 0 ;;
    *'"cancel"'*) continue ;;
    *'"synthesize"'*)
      echo '{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":3}'
      # AAAAPwAAgL8AAIA+ is base64 of the three float32s above (endian may vary); use python
      python3 -c 'import sys,struct; sys.stdout.buffer.write(struct.pack("<fff",0.5,-0.5,0.25)); sys.stdout.buffer.write(b"\n")'
      echo '{"type":"final","id":"nqwen-1"}'
      ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(b64src), 0o700); err != nil {
		t.Fatal(err)
	}
	modelDir := t.TempDir()
	session, err := startNativeQwenSession(context.Background(), script, modelDir, 5*time.Second)
	if err != nil {
		t.Fatalf("startNativeQwenSession: %v", err)
	}
	defer session.Close()
	if len(session.presets) != 1 || session.presets[0] != "Vivian" {
		t.Fatalf("presets=%v", session.presets)
	}

	ctx := context.Background()
	stream := audio.NewPCMStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		err := session.SynthesizeToStream(ctx, SynthesisRequest{Text: "hi", Voice: "Vivian"}, stream)
		if err != nil {
			stream.CloseWithError(err)
		} else {
			stream.Close()
		}
		errCh <- err
	}()
	var got []float32
	for frame := range stream.Frames() {
		got = append(got, frame...)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("SynthesizeToStream: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("samples=%v want 3", got)
	}
	for i := range samples {
		if math.Abs(float64(got[i]-samples[i])) > 1e-5 {
			t.Fatalf("sample[%d]=%v want %v", i, got[i], samples[i])
		}
	}
}

func TestNewQwen3TTSNativePath(t *testing.T) {
	script := filepath.Join(t.TempDir(), "qwen3-tts-worker")
	source := `#!/usr/bin/env bash
set -euo pipefail
echo '{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian","Ryan"]}'
while IFS= read -r line; do
  case "$line" in
    *'"shutdown"'*) exit 0 ;;
    *'"synthesize"'*)
      echo '{"type":"pcm_meta","id":"x","sample_rate":24000,"format":"f32le","n_samples":2}'
      python3 -c 'import sys,struct; sys.stdout.buffer.write(struct.pack("<ff",0.1,-0.1)); sys.stdout.buffer.write(b"\n")'
      echo '{"type":"final","id":"x"}'
      ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	modelDir := t.TempDir()
	q, err := NewQwen3TTS(&config.Config{
		QwenTTSBinary:  script,
		QwenTTSModel:   modelDir,
		QwenTTSVoice:   "Vivian",
		QwenTTSMode:    "customvoice",
		QwenTTSTimeout: 30,
	})
	if err != nil {
		t.Fatalf("NewQwen3TTS: %v", err)
	}
	defer q.Delete()
	if !q.native || q.managed {
		t.Fatalf("native=%v managed=%v", q.native, q.managed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := q.SynthesizeRequest(ctx, SynthesisRequest{Text: "hello", Voice: "Vivian"})
	if err != nil {
		t.Fatalf("SynthesizeRequest: %v", err)
	}
	var n int
	for frame := range result.Stream.Frames() {
		n += len(frame)
	}
	if n < 2 {
		t.Fatalf("got %d samples", n)
	}
	status := q.Status()
	if !status.Available || status.Detail == "" {
		t.Fatalf("status=%+v", status)
	}
	voices := q.ListVoices("", "")
	if len(voices) == 0 {
		t.Fatal("expected preset voices")
	}
}
