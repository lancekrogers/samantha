package config

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/qwen"
)

func TestEnsureRuntimeAssetsNativeQwenTar(t *testing.T) {
	dir := t.TempDir()
	archive, sum := writeTestNativeTar(t, t.TempDir())

	cfg := &Config{
		ModelsDir:           dir,
		TTSProvider:         "qwen3-tts",
		QwenTTSNativeURL:    archive,
		QwenTTSNativeSHA256: sum,
		QwenTTSModelTier:    "0.6b",
	}
	err := EnsureRuntimeAssets(context.Background(), cfg, AssetRequest{NeedTTS: true}, nil)
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	st := qwen.InspectNative(dir, "0.6b")
	if !st.Installed {
		t.Fatalf("native not installed: %+v", st)
	}
	// Second ensure is a no-op.
	if err := EnsureRuntimeAssets(context.Background(), cfg, AssetRequest{NeedTTS: true}, nil); err != nil {
		t.Fatal(err)
	}
}

func writeTestNativeTar(t *testing.T, dir string) (path, shaHex string) {
	t.Helper()
	install := map[string]any{
		"schema": "qwen3-tts-native.install.v1", "tier_default": "0.6b",
		"bin": map[string]string{"worker": "bin/qwen3-tts-worker"},
		"models": map[string]any{
			"0.6b": map[string]any{
				"tts":       map[string]string{"path": "models/qwen3-tts-0.6b-f16.gguf"},
				"tokenizer": map[string]string{"path": "models/qwen3-tts-tokenizer-f16.gguf"},
			},
		},
		"presets": "models/presets/presets.json",
	}
	b, _ := json.Marshal(install)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "pkg/"
	files := map[string]string{
		prefix + "install.json":                        string(b),
		prefix + "bin/qwen3-tts-worker":                "#!/bin/sh\n",
		prefix + "models/qwen3-tts-0.6b-f16.gguf":      "tts",
		prefix + "models/qwen3-tts-tokenizer-f16.gguf": "tok",
		prefix + "models/presets/presets.json":         `{"voices":[{"name":"Vivian"}]}`,
	}
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	path = filepath.Join(dir, "n.tar.gz")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(sum[:])
}
