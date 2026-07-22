//go:build integration

package tts

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
)

// TestLiveManagedQwenVoiceMatrix is intentionally opt-in: it loads the real
// multi-GB managed model, writes inspectable WAVs, proves two named speakers do
// not collapse to identical output, then cancels one request and verifies the
// provider starts a fresh worker for the next request.
func TestLiveManagedQwenVoiceMatrix(t *testing.T) {
	if os.Getenv("SAMANTHA_QWEN_REAL_MODEL") != "1" {
		t.Skip("set SAMANTHA_QWEN_REAL_MODEL=1 after `samantha models ensure --tts`")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TTSProvider = managedqwen.ProviderName
	cfg.QwenTTSBinary = ""
	cfg.QwenTTSModel = ""
	cfg.QwenTTSMode = string(VoiceModeCustomVoice)
	cfg.QwenTTSLanguage = managedqwen.DefaultLanguage
	if cfg.QwenTTSTimeout < 300 {
		cfg.QwenTTSTimeout = 300
	}

	provider, err := NewQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("initialize managed Qwen (install first with `samantha models ensure --tts`): %v", err)
	}
	defer provider.Delete()

	outDir := os.Getenv("SAMANTHA_QWEN_SMOKE_DIR")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hashes := make(map[string][sha256.Size]byte)
	for _, voice := range []string{"Vivian", "Ryan"} {
		result, err := provider.SynthesizeRequest(context.Background(), SynthesisRequest{
			Text: "Samantha managed voice verification.", Mode: VoiceModeCustomVoice,
			Voice: voice, Language: managedqwen.DefaultLanguage,
		})
		if err != nil {
			t.Fatalf("synthesize %s: %v", voice, err)
		}
		samples := drainStream(t, result.Stream)
		if len(samples) == 0 {
			t.Fatalf("%s returned no samples", voice)
		}
		path := filepath.Join(outDir, "qwen-"+voice+".wav")
		if err := audio.WriteWAVFloat32(path, result.SampleRate, samples); err != nil {
			t.Fatal(err)
		}
		hashes[voice] = hashFloat32(samples)
		t.Logf("%s: %s (%d samples)", voice, path, len(samples))
	}
	if hashes["Vivian"] == hashes["Ryan"] {
		t.Fatal("Vivian and Ryan produced byte-identical samples; selected speaker may be ignored")
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	interrupted, err := provider.Synthesize(cancelCtx, "This deliberately long request is canceled to verify that Samantha replaces the managed Qwen worker and remains ready for the next utterance.")
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	for range interrupted.Frames() {
	}
	if err := interrupted.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel request error = %v, want context cancellation", err)
	}
	if !provider.Available() {
		t.Fatal("provider became unavailable after cancellation")
	}

	recovered, err := provider.SynthesizeRequest(context.Background(), SynthesisRequest{
		Text: "Samantha recovered after cancellation.", Mode: VoiceModeCustomVoice,
		Voice: "Ryan", Language: "English",
	})
	if err != nil {
		t.Fatalf("synthesize after cancellation: %v", err)
	}
	recoverySamples := drainStream(t, recovered.Stream)
	recoveryPath := filepath.Join(outDir, "qwen-recovered.wav")
	if err := audio.WriteWAVFloat32(recoveryPath, recovered.SampleRate, recoverySamples); err != nil {
		t.Fatal(err)
	}
	t.Logf("recovery: %s (%d samples)", recoveryPath, len(recoverySamples))
}

func hashFloat32(samples []float32) [sha256.Size]byte {
	data := make([]byte, len(samples)*4)
	for i, sample := range samples {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(sample))
	}
	return sha256.Sum256(data)
}
