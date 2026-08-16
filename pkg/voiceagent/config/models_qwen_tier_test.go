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

	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

func fakeSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// writeFakeQwenNativeTar builds a minimal valid native package tarball with
// the given tiers, mirroring the qwen package's test fixture. It exists here
// so the persona-tier ensure path can be exercised through the real
// EnsureNative, not a stub.
func writeFakeQwenNativeTar(t *testing.T, dir string, tiers ...string) string {
	t.Helper()
	presets := `{"schema":"qwen3-tts-native.presets.v1","voices":[{"name":"Vivian","path":"presets/Vivian.q3te"}]}`
	libSuffix := ".dylib"
	if runtime.GOOS != "darwin" {
		libSuffix = ".so"
	}
	files := map[string]string{
		"bin/qwen3-tts-worker":                "#!/bin/sh\necho ready\n",
		"bin/qwen3-tts-cli":                   "#!/bin/sh\n",
		"bin/libqwen3tts" + libSuffix:         "fake-qwen-lib",
		"bin/libggml" + libSuffix:             "fake-ggml-lib",
		"models/qwen3-tts-tokenizer-f16.gguf": "gguf-tok",
		"models/presets/presets.json":         presets,
		"models/presets/Vivian.q3te":          "Q3TE",
	}
	modelEntries := map[string]any{}
	for _, tier := range tiers {
		gguf := "models/qwen3-tts-" + tier + "-f16.gguf"
		files[gguf] = "gguf-tts-" + tier
		modelEntries[tier] = map[string]any{
			"quant": "f16",
			"tts": map[string]string{
				"path": gguf, "sha256": fakeSHA(files[gguf]),
			},
			"tokenizer": map[string]string{
				"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": fakeSHA(files["models/qwen3-tts-tokenizer-f16.gguf"]),
			},
		}
	}
	install := map[string]any{
		"schema":       "qwen3-tts-native.install.v1",
		"repo_commit":  "deadbeef",
		"engine_sha":   "b3ba140",
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"tier_default": "0.6b",
		"sample_rate":  24000,
		"protocol":     "qwen3-tts-worker/v1",
		"streaming":    false,
		"bin": map[string]string{
			"worker":        "bin/qwen3-tts-worker",
			"worker_sha256": fakeSHA(files["bin/qwen3-tts-worker"]),
			"cli":           "bin/qwen3-tts-cli",
			"cli_sha256":    fakeSHA(files["bin/qwen3-tts-cli"]),
		},
		"models":         modelEntries,
		"presets":        "models/presets/presets.json",
		"presets_sha256": fakeSHA(files["models/presets/presets.json"]),
	}
	installBytes, err := json.MarshalIndent(install, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files["install.json"] = string(installBytes) + "\n"
	files["SHA256SUMS"] = "deadbeef  ./install.json\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "qwen3-tts-native-test-darwin-arm64/"
	for name, body := range files {
		hdr := &tar.Header{Name: prefix + name, Mode: 0o644, Size: int64(len(body))}
		if name == "bin/qwen3-tts-worker" || name == "bin/qwen3-tts-cli" {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The full self-serve path with nothing mocked: an installed 0.6b-only
// package, a persona needing 1.7b, a configured multi-tier URL and NO
// checksum — EnsureQwenTTSTier must upgrade through the real EnsureNative.
func TestEnsureQwenTTSTierUpgradesFromCustomURLWithoutSHA(t *testing.T) {
	modelsDir := t.TempDir()
	cfg := &Config{
		TTSProvider:      "kokoro",
		ModelsDir:        modelsDir,
		QwenTTSModelTier: "0.6b",
		QwenTTSNativeURL: writeFakeQwenNativeTar(t, t.TempDir(), "0.6b"),
	}
	if err := EnsureQwenTTSTier(context.Background(), cfg, "0.6b", nil); err != nil {
		t.Fatalf("initial 0.6b install: %v", err)
	}
	if st := qwen.InspectNative(modelsDir, "1.7b"); st.ModelReady {
		t.Fatal("1.7b unexpectedly present before upgrade")
	}

	cfg.QwenTTSNativeURL = writeFakeQwenNativeTar(t, t.TempDir(), "0.6b", "1.7b")
	if err := EnsureQwenTTSTier(context.Background(), cfg, "1.7b", nil); err != nil {
		t.Fatalf("EnsureQwenTTSTier(1.7b): %v", err)
	}
	st := qwen.InspectNative(modelsDir, "1.7b")
	if !st.Installed || !st.ModelReady {
		t.Fatalf("status = %+v, want 1.7b installed and ready", st)
	}
	// The requested tier never leaks into the caller's config.
	if cfg.QwenTTSModelTier != "0.6b" {
		t.Fatalf("cfg tier mutated to %q", cfg.QwenTTSModelTier)
	}
}
