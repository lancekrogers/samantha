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
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestInspectNativeRejectsNonExecutableWorker(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(NativeInstallPaths(modelsDir).Worker, 0o644); err != nil {
		t.Fatal(err)
	}
	st := InspectNative(modelsDir, DefaultModelTier)
	if st.Installed || st.WorkerReady {
		t.Fatalf("status=%+v, want non-executable worker rejected", st)
	}
}

func TestInspectNativeRejectsMissingRuntimeLibs(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
		t.Fatal(err)
	}
	p := NativeInstallPaths(modelsDir)
	// Simulate pre-fix Darwin package: worker + GGUF present, no libggml*.
	entries, err := os.ReadDir(p.BinDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.Name()), "libggml") {
			if err := os.Remove(filepath.Join(p.BinDir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	st := InspectNative(modelsDir, DefaultModelTier)
	if st.Installed || st.RuntimeReady {
		t.Fatalf("status=%+v, want RuntimeReady=false and not Installed", st)
	}
	if !st.WorkerReady || !st.ModelReady {
		t.Fatalf("status=%+v, want worker+model still present", st)
	}
	if !strings.Contains(st.Detail, "runtime libraries") {
		t.Fatalf("detail=%q, want runtime libraries remediation", st.Detail)
	}
}

func TestEnsureNativeReinstallsWhenRuntimeLibsMissing(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
		t.Fatal(err)
	}
	p := NativeInstallPaths(modelsDir)
	entries, err := os.ReadDir(p.BinDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.Name()), "libggml") {
			_ = os.Remove(filepath.Join(p.BinDir, e.Name()))
		}
	}
	// Second ensure with URL must reinstall because RuntimeReady is false.
	st, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{
		URL: archive, SHA256: sum, Tier: "0.6b",
	}, nil)
	if err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	if !st.Installed || !st.RuntimeReady {
		t.Fatalf("status=%+v, want reinstalled with runtime libs", st)
	}
}

func TestInspectNativeRejectsInvalidManifest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, p NativePaths)
	}{
		{
			name: "missing model file",
			mutate: func(t *testing.T, p NativePaths) {
				t.Helper()
				if err := os.Remove(filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong platform",
			mutate: func(t *testing.T, p NativePaths) {
				t.Helper()
				data, err := os.ReadFile(p.InstallJSON)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte(`"os": "`+runtime.GOOS+`"`), []byte(`"os": "wrong-os"`), 1)
				if err := os.WriteFile(p.InstallJSON, data, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "escaping path",
			mutate: func(t *testing.T, p NativePaths) {
				t.Helper()
				data, err := os.ReadFile(p.InstallJSON)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte(`models/qwen3-tts-0.6b-f16.gguf`), []byte(`../qwen3-tts-0.6b-f16.gguf`), 1)
				if err := os.WriteFile(p.InstallJSON, data, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelsDir := t.TempDir()
			archive, sum := writeFakeNativeTar(t, t.TempDir())
			if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, NativeInstallPaths(modelsDir))
			st := InspectNative(modelsDir, DefaultModelTier)
			if st.Installed {
				t.Fatalf("status=%+v, want not installed", st)
			}
			// Missing weights leave the manifest valid but model incomplete;
			// path/schema issues surface as an invalid manifest.
			if tc.name != "missing model file" && !strings.Contains(st.Detail, "manifest is invalid") {
				t.Fatalf("status=%+v, want invalid manifest", st)
			}
		})
	}
}

// VerifyNativeInstall is the only path that re-hashes package bytes. Inspect
// must stay presence-only so TUI status chips never stream multi-GB GGUFs.
func TestVerifyNativeInstallRejectsCorruptChecksum(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
		t.Fatal(err)
	}
	p := NativeInstallPaths(modelsDir)
	if err := os.WriteFile(filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Presence-only inspect still reports the package as present.
	st := InspectNative(modelsDir, DefaultModelTier)
	if !st.Installed {
		t.Fatalf("InspectNative after content corruption = %+v, want still installed (presence-only)", st)
	}
	err := VerifyNativeInstall(modelsDir)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("VerifyNativeInstall() = %v, want sha256 mismatch", err)
	}
}

// InspectNative must stay cheap even when the real package has multi-GB weights.
// A prior bug re-hashed GGUFs on every TUI View frame via ttsBadgeLabel.
func TestInspectNativeDoesNotHashWeights(t *testing.T) {
	modelsDir := t.TempDir()
	archive, sum := writeFakeNativeTar(t, t.TempDir())
	if _, err := EnsureNative(context.Background(), modelsDir, NativeEnsureOptions{URL: archive, SHA256: sum}, nil); err != nil {
		t.Fatal(err)
	}
	// Inflate the model file so a mistaken full-hash path would blow the budget.
	big := make([]byte, 32<<20) // 32 MiB
	for i := range big {
		big[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(NativeInstallPaths(modelsDir).ModelDir, "qwen3-tts-0.6b-f16.gguf"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// Warm page cache, then require inspect to stay well under a hash of 32 MiB.
	_ = InspectNative(modelsDir, DefaultModelTier)
	start := time.Now()
	for i := 0; i < 50; i++ {
		st := InspectNative(modelsDir, DefaultModelTier)
		if !st.Installed {
			t.Fatalf("InspectNative = %+v, want installed", st)
		}
	}
	elapsed := time.Since(start)
	// Hashing 50 × 32 MiB is hundreds of ms even on SSD; presence is sub-ms.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("InspectNative x50 took %v; likely re-hashing weights (budget 200ms)", elapsed)
	}
}

func TestExtractNativeTarGzRejectsUnsafeSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tar.Header
	}{
		{
			name: "target escapes package",
			entries: []tar.Header{
				{Name: "pkg/bin", Typeflag: tar.TypeSymlink, Linkname: "../escape", Mode: 0o777},
			},
		},
		{
			name: "write follows prior symlink",
			entries: []tar.Header{
				{Name: "pkg/bin/link", Typeflag: tar.TypeSymlink, Linkname: "../models", Mode: 0o777},
				{Name: "pkg/bin/link/pwned", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for i := range tc.entries {
				if err := tw.WriteHeader(&tc.entries[i]); err != nil {
					t.Fatal(err)
				}
				if tc.entries[i].Size > 0 {
					if _, err := tw.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := extractNativeTarGz(archive, filepath.Join(t.TempDir(), "qwen3-tts")); err == nil {
				t.Fatal("expected unsafe symlink extraction to fail")
			}
		})
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
	presets := `{"schema":"qwen3-tts-native.presets.v1","voices":[{"name":"Vivian","path":"presets/Vivian.q3te"}]}`
	// Portable package ships worker + libqwen3tts + libggml* (product load path).
	libSuffix := ".dylib"
	if runtime.GOOS != "darwin" {
		libSuffix = ".so"
	}
	files := map[string]string{
		"bin/qwen3-tts-worker":                "#!/bin/sh\necho ready\n",
		"bin/qwen3-tts-cli":                   "#!/bin/sh\n",
		"bin/libqwen3tts" + libSuffix:         "fake-qwen-lib",
		"bin/libggml" + libSuffix:             "fake-ggml-lib",
		"models/qwen3-tts-0.6b-f16.gguf":      "gguf-tts",
		"models/qwen3-tts-tokenizer-f16.gguf": "gguf-tok",
		"models/presets/presets.json":         presets,
		"models/presets/Vivian.q3te":          "Q3TE",
	}
	install := map[string]any{
		"schema":       nativeInstallSchema,
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
			"worker_sha256": sha256Text(files["bin/qwen3-tts-worker"]),
			"cli":           "bin/qwen3-tts-cli",
			"cli_sha256":    sha256Text(files["bin/qwen3-tts-cli"]),
		},
		"models": map[string]any{
			"0.6b": map[string]any{
				"quant": "f16",
				"tts": map[string]string{
					"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": sha256Text(files["models/qwen3-tts-0.6b-f16.gguf"]),
				},
				"tokenizer": map[string]string{
					"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": sha256Text(files["models/qwen3-tts-tokenizer-f16.gguf"]),
				},
			},
		},
		"presets":        "models/presets/presets.json",
		"presets_sha256": sha256Text(files["models/presets/presets.json"]),
	}
	installBytes, _ := json.MarshalIndent(install, "", "  ")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "qwen3-tts-native-test-darwin-arm64/"
	files["install.json"] = string(installBytes) + "\n"
	files["SHA256SUMS"] = "deadbeef  ./install.json\n"
	for name, body := range files {
		hdr := &tar.Header{Name: prefix + name, Mode: 0o644, Size: int64(len(body))}
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

func sha256Text(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
