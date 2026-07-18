//go:build integration

package tts

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
)

// Live Kokoro regression: real model, real WAV metrics, real skip logs.
//
//	just test kokoro-contractions
//	go test -tags=integration ./internal/tts/ -run LiveKokoro -count=1 -timeout 5m -v
//
// Skips when the Kokoro pack is not installed (CI without model cache).
//
// The U+0329 "Skip unknown phonemes" lines only appear for the intentional
// stock-token control synths. Production / aliased tokens must log zero skips.

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

	// --- Control: stock tokens clip "wasn't" and log U+0329 skips (captured).
	var stockWasnt []float32
	stockLog, err := withCapturedStderr(func() {
		stockWasnt = synthPCM(t, modelsDir, stockTokens, "wasn't", 10, 0.95)
	})
	if err != nil && runtime.GOOS != "windows" {
		t.Fatalf("capture stock stderr: %v", err)
	}
	stockSkips := strings.Count(stockLog, `\U+0329`) + strings.Count(stockLog, "U+0329")
	if runtime.GOOS != "windows" && stockSkips < 1 {
		t.Fatalf("stock tokens must log ≥1 U+0329 skip for wasn't (got %d); log=%q", stockSkips, stockLog)
	}

	// --- Fix path: aliased tokens keep the phone; zero U+0329 skips.
	var aliasWasnt, wasNot []float32
	aliasLog, err := withCapturedStderr(func() {
		aliasWasnt = synthPCM(t, modelsDir, tokensPath, "wasn't", 10, 0.95)
		wasNot = synthPCM(t, modelsDir, tokensPath, "was not", 10, 0.95)
	})
	if err != nil && runtime.GOOS != "windows" {
		t.Fatalf("capture alias stderr: %v", err)
	}
	aliasSkips := strings.Count(aliasLog, `\U+0329`) + strings.Count(aliasLog, "U+0329")
	if runtime.GOOS != "windows" && aliasSkips != 0 {
		t.Fatalf("aliased tokens still skip U+0329 (%d times); log=%q", aliasSkips, aliasLog)
	}

	writeWAV(t, filepath.Join(outDir, "wasnt-stock.wav"), 24000, stockWasnt)
	writeWAV(t, filepath.Join(outDir, "wasnt-alias.wav"), 24000, aliasWasnt)
	writeWAV(t, filepath.Join(outDir, "was-not.wav"), 24000, wasNot)

	if len(aliasWasnt) <= len(stockWasnt) {
		t.Fatalf("aliased wasn't (%d samples) must be longer than stock clipped wasn't (%d samples); syllabic-n alias not working",
			len(aliasWasnt), len(stockWasnt))
	}
	// Aliased contraction should be in the same ballpark as the expanded form.
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

	var samples []float32
	var sr int
	prodLog, err := withCapturedStderr(func() {
		samples, sr = synthesizeRequest(t, ctx, k, phrase)
	})
	if err != nil && runtime.GOOS != "windows" {
		t.Fatalf("capture production stderr: %v", err)
	}
	prodSkips := strings.Count(prodLog, `\U+0329`) + strings.Count(prodLog, "U+0329")
	if runtime.GOOS != "windows" && prodSkips != 0 {
		t.Fatalf("NewKokoroTTS still skips U+0329 (%d times) on contraction phrase; log=%q", prodSkips, prodLog)
	}
	writeWAV(t, filepath.Join(outDir, "phrase-contractions.wav"), sr, samples)

	if sr != 24000 {
		t.Fatalf("sample rate = %d, want 24000", sr)
	}
	dur := float64(len(samples)) / float64(sr)
	if dur < 3.0 {
		t.Fatalf("phrase duration = %.2fs, want ≥ 3.0s (got %d samples); contractions may be clipped", dur, len(samples))
	}
	rms := pcmRMS(samples)
	if rms < 0.01 {
		t.Fatalf("phrase RMS = %g, want audible speech", rms)
	}

	// Same phrase through stock tokens (clipped) must be shorter than production.
	var stockPhrase []float32
	_, _ = withCapturedStderr(func() {
		stockPhrase = synthPCM(t, modelsDir, stockTokens, phrase, 10, 0.95)
	})
	writeWAV(t, filepath.Join(outDir, "phrase-stock-tokens.wav"), 24000, stockPhrase)
	if len(samples) <= len(stockPhrase) {
		t.Fatalf("production phrase (%d samples) must exceed stock-token phrase (%d); alias not applied in NewKokoroTTS",
			len(samples), len(stockPhrase))
	}

	t.Logf("ok: stock wasn't=%d (skips=%d) alias wasn't=%d (skips=%d) was-not=%d phrase=%.2fs prod_skips=%d stock_phrase=%d rms=%.4f tokens=%s wavs=%s",
		len(stockWasnt), stockSkips, len(aliasWasnt), aliasSkips, len(wasNot), dur, prodSkips, len(stockPhrase), rms, tokensPath, outDir)
}

func TestLiveKokoroHealthyContractionsUnchanged(t *testing.T) {
	// Guards against reintroducing broad n't rewrites.
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

	// Smoke: each form produces non-trivial audio under the production path
	// with zero U+0329 skips (captured on non-windows).
	log, capErr := withCapturedStderr(func() {
		for _, word := range []string{"isn't", "don't", "won't", "can't", "wasn't"} {
			samples, sr := synthesizeRequest(t, ctx, k, word)
			if len(samples) < sr/4 { // < 250 ms is almost certainly broken
				t.Errorf("%q produced only %d samples (%.0f ms)", word, len(samples), 1000*float64(len(samples))/float64(sr))
			}
		}
	})
	if capErr == nil && runtime.GOOS != "windows" {
		if n := strings.Count(log, `\U+0329`) + strings.Count(log, "U+0329"); n != 0 {
			t.Fatalf("healthy contraction smoke still skips U+0329 (%d): %s", n, log)
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
