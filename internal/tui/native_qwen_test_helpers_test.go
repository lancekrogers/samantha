package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

func writeNativeTUITestInstall(t *testing.T, modelsDir string) {
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
	for _, voice := range managedqwen.CustomVoices() {
		path := filepath.Join(p.ModelDir, "presets", voice.Name+".q3te")
		if err := os.WriteFile(path, []byte("preset"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"schema": "qwen3-tts-native.install.v1", "os": runtime.GOOS, "arch": runtime.GOARCH,
		"tier_default": "0.6b", "sample_rate": 24000, "protocol": "qwen3-tts-worker/v1",
		"bin": map[string]string{
			"worker": "bin/qwen3-tts-worker", "worker_sha256": tuiNativeSHA(worker),
		},
		"models": map[string]any{
			"0.6b": map[string]any{
				"tts": map[string]string{
					"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": tuiNativeSHA(ttsModel),
				},
				"tokenizer": map[string]string{
					"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": tuiNativeSHA(tokenizer),
				},
			},
		},
		"presets": "models/presets/presets.json", "presets_sha256": tuiNativeSHA(presets),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.InstallJSON, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func tuiNativeSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
