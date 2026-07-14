// Golden Kokoro synth via Go samantha (sherpa-onnx offline TTS).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
	"github.com/lancekrogers/samantha/internal/tts"
)

func main() {
	text := flag.String("text", "", "text to synthesize")
	voice := flag.String("voice", "af_heart", "kokoro voice id")
	speed := flag.Float64("speed", 0.95, "speech speed")
	out := flag.String("out", "", "output WAV path")
	metaPath := flag.String("meta", "", "JSON provenance path (default: out with .json)")
	flag.Parse()
	if *text == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: synth-go -text ... -out out.wav [-voice af_heart] [-speed 0.95]")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	cfg.TTSProvider = "kokoro"
	cfg.TTSVoice = *voice
	cfg.SpeechSpeed = *speed

	k, err := tts.NewKokoroTTS(cfg)
	if err != nil {
		fail(fmt.Errorf("NewKokoroTTS: %w (models_dir=%s)", err, config.ModelsDir()))
	}
	defer k.Delete()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream, err := k.Synthesize(ctx, *text)
	if err != nil {
		fail(err)
	}

	sr, err := stream.WaitReady(ctx)
	if err != nil {
		fail(fmt.Errorf("WaitReady: %w", err))
	}

	var samples []float32
	for frame := range stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := stream.Err(); err != nil {
		fail(err)
	}
	if sr <= 0 {
		sr = 24000
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := audio.WriteWAVFloat32(*out, sr, samples); err != nil {
		fail(err)
	}

	modelsDir := config.KokoroDir()
	modelPath := filepath.Join(modelsDir, "model.onnx")
	voicesPath := filepath.Join(modelsDir, "voices.bin")
	dur := 0.0
	if sr > 0 {
		dur = float64(len(samples)) / float64(sr)
	}
	prepared := textclean.PrepareKokoroText(*text)
	meta := map[string]any{
		"stack":          "go-sherpa-onnx-kokoro",
		"voice":          *voice,
		"speed":          *speed,
		"text":           *text,
		"prepared_text":  prepared,
		"text_rewritten": prepared != *text,
		"sample_rate":    sr,
		"num_samples":    len(samples),
		"duration_s":     dur,
		"kokoro_pack":    config.KokoroPack(),
		"models_dir":     modelsDir,
		"model_path":     modelPath,
		"voices_path":    voicesPath,
		"model_sha256":   fileSHA(modelPath),
		"voices_sha256":  fileSHA(voicesPath),
		"tokens_path":    filepath.Join(modelsDir, "tokens.txt"),
		"espeak_dir":     filepath.Join(modelsDir, "espeak-ng-data"),
		"lexicon_path":   filepath.Join(modelsDir, "lexicon-us-en.txt"),
		"sherpa_go":      "github.com/k2-fsa/sherpa-onnx-go (see projects/samantha go.mod)",
	}
	fmt.Fprintf(os.Stderr, "prepared_text: %q\n", prepared)
	mp := *metaPath
	if mp == "" {
		mp = (*out)[:len(*out)-len(filepath.Ext(*out))] + ".json"
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(mp, append(b, '\n'), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (%.2fs @ %d Hz)\n", *out, dur, sr)
	fmt.Printf("meta  %s\n", mp)
}

func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
