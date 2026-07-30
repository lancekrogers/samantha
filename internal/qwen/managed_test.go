package qwen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUseManaged(t *testing.T) {
	tests := []struct {
		name, binary, model string
		want                bool
	}{
		{"empty", "", "", true},
		{"legacy cli default", "qwen3-tts-cli", "", true},
		{"explicit model", "qwen3-tts-cli", "/m", false},
		{"explicit worker", "/opt/w", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UseManaged(tt.binary, tt.model); got != tt.want {
				t.Fatalf("UseManaged(%q,%q)=%v want %v", tt.binary, tt.model, got, tt.want)
			}
		})
	}
}

func TestInspectNativeInstalled(t *testing.T) {
	dir := t.TempDir()
	// empty → not installed, not legacy
	st := Inspect(dir)
	if st.Installed || st.LegacyPython {
		t.Fatalf("%+v", st)
	}
}

func TestDetectAndQuarantineLegacyPython(t *testing.T) {
	dir := t.TempDir()
	p := ManagedPaths(dir)
	if err := os.MkdirAll(filepath.Dir(p.Worker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Worker, []byte("# legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	leg := DetectLegacyPython(dir)
	if !leg.Present {
		t.Fatal("expected legacy")
	}
	dst, err := QuarantineLegacyPython(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst == "" {
		t.Fatal("empty quarantine path")
	}
	if DetectLegacyPython(dir).Present {
		t.Fatal("legacy still present after quarantine")
	}
}

func TestQuarantineLegacyPythonBesideNativeClearsCustomVoice(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNativePackage(t, dir)
	// Co-located multi-GB style HF tree + uv leftover.
	p := ManagedPaths(dir)
	if err := os.MkdirAll(p.Model, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Model, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.BinDir, "uv"), []byte("uv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !DetectLegacyPython(dir).Present {
		t.Fatal("expected co-located legacy")
	}
	if !InspectNative(dir, DefaultModelTier).Installed {
		t.Fatal("native fixture not installed")
	}
	if _, err := QuarantineLegacyPython(dir); err != nil {
		t.Fatal(err)
	}
	if DetectLegacyPython(dir).Present {
		t.Fatal("legacy still present after co-located quarantine")
	}
	if !InspectNative(dir, DefaultModelTier).Installed {
		t.Fatal("native package must survive quarantine")
	}
}

func TestUniqueQuarantinePathSecondRun(t *testing.T) {
	dir := t.TempDir()
	p := ManagedPaths(dir)
	if err := os.MkdirAll(filepath.Dir(p.Worker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Worker, []byte("#1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst1, err := QuarantineLegacyPython(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate a full legacy tree and quarantine again.
	if err := os.MkdirAll(filepath.Dir(p.Worker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Worker, []byte("#2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst2, err := QuarantineLegacyPython(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst1 == dst2 {
		t.Fatalf("second quarantine reused path %q", dst2)
	}
}

func writeMinimalNativePackage(t *testing.T, modelsDir string) {
	t.Helper()
	p := NativeInstallPaths(modelsDir)
	worker, tts, tok, presets := "#!/bin/sh\n", "tts", "tok", `{"voices":[{"name":"Vivian"}]}`
	for path, body := range map[string]string{
		p.Worker: worker,
		filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf"):      tts,
		filepath.Join(p.ModelDir, "qwen3-tts-tokenizer-f16.gguf"): tok,
		p.PresetsJSON: presets,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(p.Worker, 0o755)
	sha := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	manifest := map[string]any{
		"schema": "qwen3-tts-native.install.v1", "os": runtime.GOOS, "arch": runtime.GOARCH,
		"tier_default": "0.6b", "sample_rate": 24000, "protocol": "qwen3-tts-worker/v1",
		"bin": map[string]string{"worker": "bin/qwen3-tts-worker", "worker_sha256": sha(worker)},
		"models": map[string]any{
			"0.6b": map[string]any{
				"tts":       map[string]string{"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": sha(tts)},
				"tokenizer": map[string]string{"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": sha(tok)},
			},
		},
		"presets": "models/presets/presets.json", "presets_sha256": sha(presets),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.InstallJSON, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalVoice(t *testing.T) {
	if v, ok := CanonicalVoice("vivian"); !ok || v != "Vivian" {
		t.Fatalf("%v %v", v, ok)
	}
}
