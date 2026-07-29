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
	"runtime"
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
	files := map[string]string{
		"bin/qwen3-tts-worker":                "#!/bin/sh\n",
		"models/qwen3-tts-0.6b-f16.gguf":      "tts",
		"models/qwen3-tts-tokenizer-f16.gguf": "tok",
		"models/presets/presets.json":         `{"voices":[{"name":"Vivian"}]}`,
	}
	install := map[string]any{
		"schema": "qwen3-tts-native.install.v1", "tier_default": "0.6b",
		"os": runtime.GOOS, "arch": runtime.GOARCH,
		"sample_rate": 24000, "protocol": "qwen3-tts-worker/v1",
		"bin": map[string]string{
			"worker": "bin/qwen3-tts-worker", "worker_sha256": testNativeSHA(files["bin/qwen3-tts-worker"]),
		},
		"models": map[string]any{
			"0.6b": map[string]any{
				"tts": map[string]string{
					"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": testNativeSHA(files["models/qwen3-tts-0.6b-f16.gguf"]),
				},
				"tokenizer": map[string]string{
					"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": testNativeSHA(files["models/qwen3-tts-tokenizer-f16.gguf"]),
				},
			},
		},
		"presets": "models/presets/presets.json", "presets_sha256": testNativeSHA(files["models/presets/presets.json"]),
	}
	b, _ := json.Marshal(install)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "pkg/"
	files["install.json"] = string(b)
	for name, body := range files {
		hdr := &tar.Header{Name: prefix + name, Mode: 0o755, Size: int64(len(body))}
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

func testNativeSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
