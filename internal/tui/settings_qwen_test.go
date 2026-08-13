package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

func TestBuildQwenItemsTierConsentCache(t *testing.T) {
	dir := t.TempDir()
	writeNativeTUITestInstall(t, dir)
	cache := managedqwen.NativeInstallPaths(dir).CacheDir
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "abc.emb"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ModelsDir:        dir,
		TTSProvider:      "qwen3-tts",
		QwenTTSModelTier: "0.6b",
		QwenTTSConsent:   false,
		QwenTTSVoice:     "Vivian",
	}
	m := settingsModel{cfg: cfg, saveConfig: func(string, any) error { return nil }}
	m.buildQwenItems()
	if len(m.qwenItems) < 4 {
		t.Fatalf("qwen items = %d, want >= 4", len(m.qwenItems))
	}
	joined := ""
	for _, it := range m.qwenItems {
		joined += it.label + "\n"
	}
	if !strings.Contains(joined, "native ready") && !strings.Contains(joined, "Package") {
		t.Fatalf("status row missing: %s", joined)
	}
	if !strings.Contains(joined, "0.6b") {
		t.Fatalf("tier row missing 0.6b: %s", joined)
	}
	if !strings.Contains(joined, "Clone consent") || !strings.Contains(joined, "OFF") {
		t.Fatalf("consent row: %s", joined)
	}
	if !strings.Contains(joined, "1 file") {
		t.Fatalf("cache count: %s", joined)
	}

	// Toggle consent via select
	m.cursor = 2 // consent row (status=0, tier=1, consent=2)
	if m.qwenItems[m.cursor].kind != qwenOptConsent {
		for i, it := range m.qwenItems {
			if it.kind == qwenOptConsent {
				m.cursor = i
				break
			}
		}
	}
	m.selectQwenItem()
	if !m.cfg.QwenTTSConsent {
		t.Fatal("consent should be ON after toggle")
	}
}

func TestQwenPresetVoicesFromNative(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	presets := filepath.Join(models, "presets")
	if err := os.MkdirAll(presets, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Vivian", "Ryan"} {
		if err := os.WriteFile(filepath.Join(presets, name+".q3te"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	native := managedqwen.NativeStatus{Installed: true, ModelDir: models}
	voices := qwenPresetVoices(&config.Config{TTSProvider: "qwen3-tts"}, native)
	if len(voices) < 2 {
		t.Fatalf("voices = %+v", voices)
	}
}

func TestClearEmbeddingCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := clearEmbeddingCache(dir)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if countEmbeddingCache(dir) != 0 {
		t.Fatal("cache not empty")
	}
}
