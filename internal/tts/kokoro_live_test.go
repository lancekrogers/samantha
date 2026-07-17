//go:build integration

package tts

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
)

// Live Kokoro regression: real model, real WAV metrics.
//
//	go test -tags=integration ./internal/tts/ -run LiveKokoro -count=1 -timeout 5m -v
//
// Skips when the Kokoro pack is not installed (CI without model cache).

func TestLiveKokoroContractionsProduceFullAudio(t *testing.T) {
	cfg := requireKokoroLive(t)
	modelsDir := config.KokoroDir()
	outDir := t.TempDir()

	// Orthography must stay human — no was-n't / was not rewrites.
	phrase := "It wasn't supposed to happen, and she isn't ready. Don't worry."
	if got := textclean.PrepareKokoroText(phrase); got != phrase {
		t.Fatalf("PrepareKokoroText rewrote natural contractions:\n got %q\nwant %q", got, phrase)
	}

	// Token alias must be installed for this pack.
	tokensPath, err := ensureKokoroTokensWithSyllabicN(modelsDir)
	if err != nil {
		t.Fatalf("ensureKokoroTokensWithSyllabicN: %v", err)
	}
	raw, err := os.ReadFile(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\u0329") {
		t.Fatalf("tokens %s missing U+0329 syllabic-n alias", tokensPath)
	}

	stockTokens := filepath.Join(modelsDir, "tokens.txt")
	// "wasn't" alone: stock tokens drop syllabic-n; aliased tokens keep it.
	stockWasnt := synthPCM(t, modelsDir, stockTokens, "wasn't", 10, 0.95)
	aliasWasnt := synthPCM(t, modelsDir, tokensPath, "wasn't", 10, 0.95)
	wasNot := synthPCM(t, modelsDir, tokensPath, "was not", 10, 0.95)

	writeWAV(t, filepath.Join(outDir, "wasnt-stock.wav"), 24000, stockWasnt)
	writeWAV(t, filepath.Join(outDir, "wasnt-alias.wav"), 24000, aliasWasnt)
	writeWAV(t, filepath.Join(outDir, "was-not.wav"), 24000, wasNot)

	if len(aliasWasnt) <= len(stockWasnt) {
		t.Fatalf("aliased wasn't (%d samples) must be longer than stock clipped wasn't (%d samples); syllabic-n alias not working",
			len(aliasWasnt), len(stockWasnt))
	}
	// Aliased contraction should be in the same ballpark as the expanded form
	// (human contraction, not a clipped stump and not a different sentence).
	ratio := float64(len(aliasWasnt)) / float64(len(wasNot))
	if ratio < 0.90 || ratio > 1.15 {
		t.Fatalf("wasn't/was-not sample ratio = %.3f (alias=%d wasNot=%d); want ~1.0 ±15%%",
			ratio, len(aliasWasnt), len(wasNot))
	}

	// Full production path: NewKokoroTTS uses the alias and leaves text alone.
	k, err := NewKokoroTTS(cfg)
	if err != nil {
		t.Fatalf("NewKokoroTTS: %v", err)
	}
	t.Cleanup(k.Delete)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	samples, sr := synthesizeRequest(t, ctx, k, phrase)
	writeWAV(t, filepath.Join(outDir, "phrase-contractions.wav"), sr, samples)

	if sr != 24000 {
		t.Fatalf("sample rate = %d, want 24000", sr)
	}
	// ~3.8s measured with alias. Require substantial speech energy and duration.
	// (Do not use crackle MidSilence on natural speech — pauses are normal.)
	dur := float64(len(samples)) / float64(sr)
	if dur < 3.0 {
		t.Fatalf("phrase duration = %.2fs, want ≥ 3.0s (got %d samples); contractions may be clipped", dur, len(samples))
	}
	rms := pcmRMS(samples)
	if rms < 0.01 {
		t.Fatalf("phrase RMS = %g, want audible speech", rms)
	}

	// Same phrase through stock tokens (clipped) must be shorter than production path.
	stockPhrase := synthPCM(t, modelsDir, stockTokens, phrase, 10, 0.95)
	writeWAV(t, filepath.Join(outDir, "phrase-stock-tokens.wav"), 24000, stockPhrase)
	if len(samples) <= len(stockPhrase) {
		t.Fatalf("production phrase (%d samples) must exceed stock-token phrase (%d); alias not applied in NewKokoroTTS",
			len(samples), len(stockPhrase))
	}

	t.Logf("ok: stock wasn't=%d alias wasn't=%d was-not=%d phrase=%.2fs (stock phrase samples=%d) rms=%.4f tokens=%s wavs=%s",
		len(stockWasnt), len(aliasWasnt), len(wasNot), dur, len(stockPhrase), rms, tokensPath, outDir)
}

func TestLiveKokoroHealthyContractionsUnchanged(t *testing.T) {
	// Guards against reintroducing broad n't rewrites. Only needs models for
	// synth smoke; orthography check is free.
	_ = requireKokoroLive(t)

	keep := []string{
		"isn't", "don't", "won't", "can't", "wouldn't", "shouldn't",
		"It isn't done and we won't stop.",
	}
	for _, s := range keep {
		if got := textclean.PrepareKokoroText(s); got != s {
			t.Errorf("PrepareKokoroText(%q) = %q, want unchanged", s, got)
		}
	}

	cfg := requireKokoroLive(t)
	k, err := NewKokoroTTS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Delete)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// Smoke: each form produces non-trivial audio under the production path.
	for _, word := range []string{"isn't", "don't", "won't", "can't", "wasn't"} {
		samples, sr := synthesizeRequest(t, ctx, k, word)
		if len(samples) < sr/4 { // < 250 ms is almost certainly broken
			t.Errorf("%q produced only %d samples (%.0f ms)", word, len(samples), 1000*float64(len(samples))/float64(sr))
		}
	}
}

func requireKokoroLive(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.TTSProvider = "kokoro"
	if cfg.TTSVoice == "" {
		cfg.TTSVoice = "af_sky"
	}
	if cfg.SpeechSpeed <= 0 {
		cfg.SpeechSpeed = 0.95
	}
	dir := config.KokoroDir()
	for _, name := range []string{"model.onnx", "voices.bin", "tokens.txt", "espeak-ng-data"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Skipf("kokoro pack incomplete under %s (%s missing); skip live test", dir, name)
		}
	}
	return cfg
}

func synthPCM(t *testing.T, modelsDir, tokensPath, text string, sid int, speed float32) []float32 {
	t.Helper()
	cfg := sherpa.OfflineTtsKokoroModelConfig{
		Model:       filepath.Join(modelsDir, "model.onnx"),
		Voices:      filepath.Join(modelsDir, "voices.bin"),
		Tokens:      tokensPath,
		DataDir:     filepath.Join(modelsDir, "espeak-ng-data"),
		DictDir:     filepath.Join(modelsDir, "dict"),
		Lexicon:     filepath.Join(modelsDir, "lexicon-us-en.txt"),
		LengthScale: 1,
	}
	if _, err := os.Stat(cfg.DictDir); err != nil {
		cfg.DictDir = filepath.Join(config.ModelsDir(), "dict")
	}
	if _, err := os.Stat(cfg.Lexicon); err != nil {
		cfg.Lexicon = ""
	}
	tts := sherpa.NewOfflineTts(&sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{Kokoro: cfg},
	})
	if tts == nil {
		t.Fatalf("NewOfflineTts nil (tokens=%s)", tokensPath)
	}
	defer sherpa.DeleteOfflineTts(tts)

	audioRes := tts.Generate(text, sid, speed)
	if audioRes == nil || len(audioRes.Samples) == 0 {
		t.Fatalf("Generate(%q) returned empty audio", text)
	}
	// Copy — sherpa may reuse buffers.
	out := make([]float32, len(audioRes.Samples))
	copy(out, audioRes.Samples)
	return out
}

func synthesizeRequest(t *testing.T, ctx context.Context, k *KokoroTTS, text string) ([]float32, int) {
	t.Helper()
	res, err := k.SynthesizeRequest(ctx, SynthesisRequest{Text: text})
	if err != nil {
		t.Fatalf("SynthesizeRequest(%q): %v", text, err)
	}
	sr, err := res.Stream.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	var samples []float32
	for frame := range res.Stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := res.Stream.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if len(samples) == 0 {
		t.Fatalf("no samples for %q", text)
	}
	return samples, sr
}

func writeWAV(t *testing.T, path string, sr int, samples []float32) {
	t.Helper()
	if err := audio.WriteWAVFloat32(path, sr, samples); err != nil {
		t.Fatalf("WriteWAV %s: %v", path, err)
	}
}

func pcmRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}
