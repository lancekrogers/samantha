package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// TestProductPathNeverSelectsPythonWorker locks the cutover: empty binary/model
// (managed selection) must not resolve to a Python interpreter or worker.py.
// When the native package is missing, construction fails closed without spawning.
func TestProductPathNeverSelectsPythonWorker(t *testing.T) {
	cfg := &config.Config{
		TTSProvider: managedqwen.ProviderName,
		ModelsDir:   t.TempDir(), // no native package
	}
	_, err := NewQwen3TTS(cfg)
	if err == nil {
		t.Fatal("expected error when native package is missing")
	}
	msg := err.Error()
	if strings.Contains(msg, "worker.py") || strings.Contains(msg, "qwen_worker.py") {
		t.Fatalf("error must not direct users to Python worker scripts: %v", err)
	}
	if !strings.Contains(strings.ToLower(msg), "native") {
		t.Fatalf("error should require native package: %v", err)
	}
}

// TestManagedSelectionResolvesNativeWorkerNotPython asserts product selection
// (empty binary/model) resolves to qwen3-tts-worker under the native package,
// never a Python interpreter or .py script. (Does not start the worker process.)
func TestManagedSelectionResolvesNativeWorkerNotPython(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNativeInstall(t, dir)
	if !managedqwen.UseManaged("", "") {
		t.Fatal("empty binary/model should be managed selection")
	}
	install, ok := findNativeInstall(dir, managedqwen.DefaultModelTier)
	if !ok {
		t.Fatal("native install not found")
	}
	base := strings.ToLower(filepath.Base(install.Worker))
	if base != "qwen3-tts-worker" && base != "qwen3-tts-worker.exe" {
		t.Fatalf("resolved worker = %q, want qwen3-tts-worker", install.Worker)
	}
	if strings.Contains(base, "python") || strings.HasSuffix(base, ".py") {
		t.Fatalf("product worker is Python: %q", install.Worker)
	}
	st := managedqwen.Inspect(dir)
	if !st.Installed {
		t.Fatalf("Inspect product status = %+v, want native Installed", st)
	}
	workerBase := strings.ToLower(filepath.Base(st.Worker))
	if workerBase == "python" || workerBase == "python3" || strings.HasSuffix(workerBase, ".py") {
		t.Fatalf("Inspect.Worker is Python: %q", st.Worker)
	}
}

// TestSourceTreeHasNoEmbeddedQwenWorkerPy scans product packages for go:embed of
// a Qwen worker script (static guard against reintroducing the embed).
func TestSourceTreeHasNoEmbeddedQwenWorkerPy(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/tts → repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	forbidden := []string{
		"//go:embed worker.py",
		"//go:embed qwen_worker.py",
		"go:embed worker.py",
		"go:embed qwen_worker.py",
	}
	walkRoots := []string{
		filepath.Join(root, "pkg", "voiceagent", "qwen"),
		filepath.Join(root, "pkg", "voiceagent", "tts"),
		filepath.Join(root, "pkg", "voiceagent", "config"),
	}
	for _, dir := range walkRoots {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			s := string(data)
			for _, f := range forbidden {
				if strings.Contains(s, f) {
					t.Errorf("%s embeds Qwen Python worker (%q)", path, f)
				}
			}
			// Production ensure code must not shell out to uv installers.
			if strings.Contains(path, filepath.Join("config", "models.go")) {
				if strings.Contains(s, "uv tool install") || strings.Contains(s, `"uv"`) && strings.Contains(s, "exec.Command") {
					t.Errorf("%s appears to use uv for product ensure", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"worker.py", "qwen_worker.py"} {
		p := filepath.Join(root, "pkg", "voiceagent", "qwen", name)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("forbidden product worker script present: %s", p)
		}
	}
}

func writeMinimalNativeInstall(t *testing.T, modelsDir string) {
	t.Helper()
	p := managedqwen.NativeInstallPaths(modelsDir)
	worker := "#!/bin/sh\n"
	ttsModel := "tts"
	tokenizer := "tokenizer"
	presets := `{"voices":[{"name":"Vivian"}]}`
	libExt := ".dylib"
	if runtime.GOOS != "darwin" {
		libExt = ".so"
	}
	for path, body := range map[string]string{
		p.Worker: worker,
		filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf"):      ttsModel,
		filepath.Join(p.ModelDir, "qwen3-tts-tokenizer-f16.gguf"): tokenizer,
		p.PresetsJSON: presets,
		filepath.Join(p.BinDir, "libqwen3tts"+libExt): "lib",
		filepath.Join(p.BinDir, "libggml"+libExt):     "ggml",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(p.Worker, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema": "qwen3-tts-native.install.v1", "os": runtime.GOOS, "arch": runtime.GOARCH,
		"tier_default": "0.6b", "sample_rate": 24000, "protocol": "qwen3-tts-worker/v1",
		"bin": map[string]string{
			"worker": "bin/qwen3-tts-worker", "worker_sha256": nativeGuardSHA(worker),
		},
		"models": map[string]any{
			"0.6b": map[string]any{
				"tts": map[string]string{
					"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": nativeGuardSHA(ttsModel),
				},
				"tokenizer": map[string]string{
					"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": nativeGuardSHA(tokenizer),
				},
			},
		},
		"presets": "models/presets/presets.json", "presets_sha256": nativeGuardSHA(presets),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.InstallJSON, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !managedqwen.InspectNative(modelsDir, managedqwen.DefaultModelTier).Installed {
		t.Fatal("fixture install not recognized by InspectNative")
	}
}

func nativeGuardSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
