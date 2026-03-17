package stt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/config"
)

func TestWhisperCPPTranscribeInvokesCLIAndNormalizesText(t *testing.T) {
	tempDir := t.TempDir()
	modelPath := filepath.Join(tempDir, "ggml-base.en.bin")
	if err := os.WriteFile(modelPath, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	binaryPath := filepath.Join(tempDir, "whisper-cli")
	script := `#!/bin/sh
set -eu
prefix=""
log=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -of)
      prefix="$2"
      shift 2
      ;;
    -m)
      log="$log model=$2"
      shift 2
      ;;
    -f)
      log="$log file=$2"
      shift 2
      ;;
    -l)
      log="$log lang=$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
printf "%s" "$log" > "` + tempDir + `/args.log"
printf "HELLO SAMANTHA" > "${prefix}.txt"
`
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Language:            "en-US",
		WhisperCPPBinary:    binaryPath,
		WhisperCPPModelPath: modelPath,
	}
	provider, err := NewWhisperCPPSTT(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewWhisperCPPSTT() error = %v", err)
	}

	samples := make([]float32, audio.SampleRate/8)
	for i := range samples {
		samples[i] = 0.2
	}

	text, err := provider.transcribe(context.Background(), samples)
	if err != nil {
		t.Fatalf("transcribe() error = %v", err)
	}
	if text != "Hello samantha" {
		t.Fatalf("transcribe() = %q, want %q", text, "Hello samantha")
	}

	argsLog, err := os.ReadFile(filepath.Join(tempDir, "args.log"))
	if err != nil {
		t.Fatalf("ReadFile(args.log) error = %v", err)
	}
	logText := string(argsLog)
	for _, want := range []string{"model=" + modelPath, "lang=en"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("args log %q missing %q", logText, want)
		}
	}
}

func TestWhisperCPPLanguageExtractsBaseLanguage(t *testing.T) {
	if got := whisperCPPLanguage("en-US"); got != "en" {
		t.Fatalf("whisperCPPLanguage() = %q, want %q", got, "en")
	}
	if got := whisperCPPLanguage(""); got != "" {
		t.Fatalf("whisperCPPLanguage(\"\") = %q, want empty", got)
	}
}
