package qwen

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
	"strings"
	"testing"
)

func TestInspectNativeEmpty(t *testing.T) {
	st := InspectNative(t.TempDir(), "")
	if st.Installed {
		t.Fatal("expected not installed")
	}
	if st.Detail == "" {
		t.Fatal("expected detail")
	}
}

func TestEnsureNativeFromLocalTarGz(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())

	st, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{
		URL:    archive,
		SHA256: sum,
		Tier:   "0.6b",
	}, nil)
	if err != nil {
		t.Fatalf("EnsureNative: %v", err)
	}
	if !st.Installed || !st.WorkerReady || !st.ModelReady || !st.PresetsReady {
		t.Fatalf("status=%+v", st)
	}
	p := NativeInstallPaths(modelsDir)
	if !regularFile(p.Worker) || !regularFile(p.InstallJSON) {
		t.Fatal("expected worker and install.json")
	}
	if !regularFile(filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf")) {
		t.Fatal("expected 0.6b gguf")
	}
	// Idempotent second call.
	st2, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{Tier: "0.6b"}, nil)
	if err != nil || !st2.Installed {
		t.Fatalf("second ensure: %v status=%+v", err, st2)
	}
}

func TestEnsureNativeTier17BFailClosedWhenMissing(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{
		URL: archive, SHA256: sum, Tier: "0.6b",
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{
		Tier: "1.7b",
	}, nil)
	if err == nil {
		t.Fatal("expected 1.7b fail-closed when not in package")
	}
}

func TestEnsureNativeSHA256Mismatch(t *testing.T) {
	modelsDir := t.TempDir()
	archive, _ := writeFakeNativeTar(t, t.TempDir())
	_, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{
		URL: archive, SHA256: strings.Repeat("ab", 32),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("error = %v, want sha256 mismatch", err)
	}
}

func TestNormalizeTier(t *testing.T) {
	if normalizeTier("0.6B") != DefaultModelTier {
		t.Fatal(normalizeTier("0.6B"))
	}
	if normalizeTier("1.7") != Tier1_7B {
		t.Fatal(normalizeTier("1.7"))
	}
}

func writeFakeNativeTar(t *testing.T, dir string) (path, shaHex string) {
	t.Helper()
	install := map[string]any{
		"schema":       nativeInstallSchema,
		"repo_commit":  "deadbeef",
		"engine_sha":   "b3ba140",
		"os":           "darwin",
		"arch":         "arm64",
		"tier_default": "0.6b",
		"sample_rate":  24000,
		"protocol":     "qwen3-tts-worker/v1",
		"streaming":    false,
		"bin": map[string]string{
			"worker": "bin/qwen3-tts-worker",
			"cli":    "bin/qwen3-tts-cli",
		},
		"models": map[string]any{
			"0.6b": map[string]any{
				"quant": "f16",
				"tts": map[string]string{
					"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": "aa",
				},
				"tokenizer": map[string]string{
					"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": "bb",
				},
			},
		},
		"presets": "models/presets/presets.json",
	}
	installBytes, _ := json.MarshalIndent(install, "", "  ")
	presets := `{"schema":"qwen3-tts-native.presets.v1","voices":[{"name":"Vivian","path":"presets/Vivian.q3te"}]}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "qwen3-tts-native-test-darwin-arm64/"
	files := map[string]string{
		prefix + "install.json":                        string(installBytes) + "\n",
		prefix + "SHA256SUMS":                          "deadbeef  ./install.json\n",
		prefix + "bin/qwen3-tts-worker":                "#!/bin/sh\necho ready\n",
		prefix + "bin/qwen3-tts-cli":                   "#!/bin/sh\n",
		prefix + "models/qwen3-tts-0.6b-f16.gguf":      "gguf-tts",
		prefix + "models/qwen3-tts-tokenizer-f16.gguf": "gguf-tok",
		prefix + "models/presets/presets.json":         presets,
		prefix + "models/presets/Vivian.q3te":          "Q3TE",
	}
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if strings.HasSuffix(name, "worker") || strings.HasSuffix(name, "cli") {
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
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	path = filepath.Join(dir, "native.tar.gz")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(sum[:])
}
