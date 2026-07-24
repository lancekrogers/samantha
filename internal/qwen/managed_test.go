package qwen

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectManagedInstall(t *testing.T) {
	dir := t.TempDir()
	p := ManagedPaths(dir)
	for _, path := range []string{
		p.Python,
		p.Worker,
		p.RuntimeMarker,
		filepath.Join(p.Model, "config.json"),
		filepath.Join(p.Model, "model.safetensors"),
		filepath.Join(p.Model, "speech_tokenizer", "model.safetensors"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker, err := json.Marshal(installMarker{
		Schema: managedSchema, Package: PackageVersion, Worker: WorkerRevision, ModelID: DefaultModelID,
		ModelRevision: DefaultModelRevision, InstalledAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Marker, marker, 0o600); err != nil {
		t.Fatal(err)
	}

	status := Inspect(dir)
	if !status.Installed || !status.RuntimeReady || !status.ModelReady {
		t.Fatalf("Inspect() = %+v, want ready managed install", status)
	}
	if status.ModelID != DefaultModelID || status.ModelRevision != DefaultModelRevision {
		t.Fatalf("Inspect() identity = %s@%s", status.ModelID, status.ModelRevision)
	}

	if err := os.Remove(filepath.Join(p.Model, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
	status = Inspect(dir)
	if status.Installed || status.ModelReady || status.Detail == "" {
		t.Fatalf("Inspect() accepted incomplete model: %+v", status)
	}
}

func TestEnsureInstalledRuntimeIsNoOp(t *testing.T) {
	dir := t.TempDir()
	p := ManagedPaths(dir)
	for _, path := range []string{
		p.Python, p.Worker, p.RuntimeMarker, filepath.Join(p.Model, "config.json"),
		filepath.Join(p.Model, "model.safetensors"),
		filepath.Join(p.Model, "speech_tokenizer", "model.safetensors"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker, _ := json.Marshal(installMarker{
		Schema: managedSchema, Package: PackageVersion, Worker: WorkerRevision, ModelID: DefaultModelID,
		ModelRevision: DefaultModelRevision,
	})
	if err := os.WriteFile(p.Marker, marker, 0o600); err != nil {
		t.Fatal(err)
	}

	var stage string
	status, err := Ensure(context.Background(), dir, func(name string, pct float64) {
		stage = name
	})
	if err != nil || !status.Installed {
		t.Fatalf("Ensure() = %+v, %v", status, err)
	}
	if stage != "Qwen preset voices" {
		t.Fatalf("progress stage = %q", stage)
	}
}

func TestAdoptLegacyManagedInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// UserHomeDir on macOS uses HOME.

	legacyModels := filepath.Join(home, ".cache", "samantha", "models")
	writeReadyManagedInstall(t, legacyModels)

	activeModels := filepath.Join(home, ".cache", "festival-voice", "models")
	if err := adoptLegacyManagedInstall(activeModels); err != nil {
		t.Fatal(err)
	}
	status := Inspect(activeModels)
	if !status.Installed {
		t.Fatalf("adopted install not ready: %+v", status)
	}
	// Second adopt must not clobber.
	if err := adoptLegacyManagedInstall(activeModels); err == nil {
		t.Fatal("expected adopt to refuse existing destination")
	}
}

func writeReadyManagedInstall(t *testing.T, modelsDir string) {
	t.Helper()
	p := ManagedPaths(modelsDir)
	for _, path := range []string{
		p.Python, p.Worker, p.RuntimeMarker,
		filepath.Join(p.Model, "config.json"),
		filepath.Join(p.Model, "model.safetensors"),
		filepath.Join(p.Model, "speech_tokenizer", "model.safetensors"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker, err := json.Marshal(installMarker{
		Schema: managedSchema, Package: PackageVersion, Worker: WorkerRevision, ModelID: DefaultModelID,
		ModelRevision: DefaultModelRevision, InstalledAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Marker, marker, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCustomVoiceRegistryIsStable(t *testing.T) {
	voices := CustomVoices()
	if len(voices) != 9 || voices[0].Name != "Vivian" || voices[6].Name != "Aiden" {
		t.Fatalf("CustomVoices() = %+v", voices)
	}
	voices[0].Name = "mutated"
	if CustomVoices()[0].Name != "Vivian" {
		t.Fatal("CustomVoices returned mutable backing storage")
	}
}

func TestCanonicalManagedSelections(t *testing.T) {
	if got, ok := CanonicalVoice(" rYaN "); !ok || got != "Ryan" {
		t.Fatalf("CanonicalVoice() = %q, %v; want Ryan, true", got, ok)
	}
	if got, ok := CanonicalLanguage(" english "); !ok || got != "English" {
		t.Fatalf("CanonicalLanguage() = %q, %v; want English, true", got, ok)
	}
	if _, ok := CanonicalVoice("not-a-voice"); ok {
		t.Fatal("CanonicalVoice accepted an unknown voice")
	}
	if _, ok := CanonicalLanguage("Klingon"); ok {
		t.Fatal("CanonicalLanguage accepted an unknown language")
	}
}

func TestUseManagedMigratesLegacyDefault(t *testing.T) {
	tests := []struct {
		name   string
		binary string
		model  string
		want   bool
	}{
		{name: "new default", want: true},
		{name: "legacy default", binary: "qwen3-tts-cli", want: true},
		{name: "legacy absolute default", binary: "/usr/local/bin/qwen3-tts-cli", want: true},
		{name: "explicit worker without model", binary: "/opt/qwen/worker", want: false},
		{name: "explicit model", binary: "qwen3-tts-cli", model: "/opt/qwen/model", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := UseManaged(test.binary, test.model); got != test.want {
				t.Fatalf("UseManaged(%q, %q) = %v, want %v", test.binary, test.model, got, test.want)
			}
		})
	}
}
