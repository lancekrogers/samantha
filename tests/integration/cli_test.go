//go:build integration
// +build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestCLI_Help(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("--help")
	if err != nil {
		t.Fatalf("samantha --help failed: %v", err)
	}

	if !strings.Contains(output, "samantha") && !strings.Contains(output, "Samantha") {
		t.Errorf("help output should mention samantha, got: %s", output)
	}
}

func TestCLI_ConfigView(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("config")
	if err != nil {
		t.Fatalf("samantha config failed: %v", err)
	}

	// Should show default config values.
	expectedKeys := []string{"tts_provider", "stt_provider", "vad_enabled"}
	for _, key := range expectedKeys {
		if !strings.Contains(output, key) {
			t.Errorf("config output should contain %q, got: %s", key, output)
		}
	}
}

func TestCLI_ConfigSet(t *testing.T) {
	tc := GetSharedContainer(t)

	// Set a config value.
	_, err := tc.RunSamantha("config", "tts_voice", "af_bella")
	if err != nil {
		t.Fatalf("samantha config set failed: %v", err)
	}

	// Verify the config file was written.
	content, err := tc.ReadFile(filepath.Join("/root/.obey/agents/voice", config.AppSlug, "config.yaml"))
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if !strings.Contains(content, "af_bella") {
		t.Errorf("config file should contain af_bella, got: %s", content)
	}
}

func TestCLI_Providers(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("providers")
	if err != nil {
		t.Fatalf("samantha providers failed: %v", err)
	}

	if !strings.Contains(output, "kokoro") {
		t.Errorf("providers output should mention kokoro, got: %s", output)
	}
}

func TestCLI_ModelsStatus(t *testing.T) {
	tc := GetSharedContainer(t)

	// models status is read-only and offline: in the container the models are
	// absent, so it must report them missing without attempting a download.
	output, err := tc.RunSamantha("models", "status")
	if err != nil {
		t.Fatalf("samantha models status failed: %v", err)
	}

	for _, want := range []string{"Model assets", "missing"} {
		if !strings.Contains(output, want) {
			t.Errorf("models status output should contain %q, got: %s", want, output)
		}
	}
}

func TestCLI_ModelsStatusJSON(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("models", "status", "--json")
	if err != nil {
		t.Fatalf("samantha models status --json failed: %v", err)
	}

	if !strings.Contains(output, "\"installed\"") {
		t.Errorf("models status --json should emit an installed field, got: %s", output)
	}
}

func TestCLI_RenderHelp(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("render", "--help")
	if err != nil {
		t.Fatalf("samantha render --help failed: %v", err)
	}
	for _, want := range []string{"render", "--stdin", "--out"} {
		if !strings.Contains(output, want) {
			t.Errorf("render --help should document %q, got: %s", want, output)
		}
	}
}

func TestCLI_RenderRequiresOutput(t *testing.T) {
	tc := GetSharedContainer(t)

	// --stdin with no --out/--out-dir is an invalid combination; the command
	// must fail before doing any work.
	if _, err := tc.RunSamantha("render", "--stdin"); err == nil {
		t.Fatal("samantha render --stdin without an output should fail")
	}
}

func TestCLI_AudiobookCreateHelp(t *testing.T) {
	tc := GetSharedContainer(t)

	output, err := tc.RunSamantha("audiobook", "create", "--help")
	if err != nil {
		t.Fatalf("samantha audiobook create --help failed: %v", err)
	}
	for _, want := range []string{"EPUB", "--out-dir", "--resume", "--audio-format"} {
		if !strings.Contains(output, want) {
			t.Errorf("audiobook create --help should document %q, got: %s", want, output)
		}
	}
}

func TestCLI_AudiobookCreatePlan(t *testing.T) {
	tc := GetSharedContainer(t)

	// The integration binary uses the plan-only render runner, so a valid
	// create invocation reports the plan without synthesizing audio.
	output, err := tc.RunSamantha("audiobook", "create", "book.epub", "--out-dir", "out/book", "--json")
	if err != nil {
		t.Fatalf("samantha audiobook create failed: %v", err)
	}
	if !strings.Contains(output, "out/book") {
		t.Errorf("plan output should mention out/book, got: %s", output)
	}
}

func TestCLI_Doctor(t *testing.T) {
	tc := GetSharedContainer(t)

	// Select the offline-valid brain configuration so this test isolates model
	// asset warnings. The default Claude provider intentionally fails doctor
	// when its required CLI is absent from this minimal container.
	if _, err := tc.RunSamantha("config", "brain_provider", "ollama"); err != nil {
		t.Fatalf("configure offline doctor brain provider: %v", err)
	}
	if _, err := tc.RunSamantha("config", "ollama_model", "integration-model"); err != nil {
		t.Fatalf("configure offline doctor model: %v", err)
	}

	// The container has sherpa + kokoro configured but no model assets, so
	// doctor must report missing assets as warnings — and exit 0, read-only,
	// with no network — since warnings are not errors.
	output, err := tc.RunSamantha("doctor")
	if err != nil {
		t.Fatalf("samantha doctor failed (warnings should exit 0): %v", err)
	}

	for _, want := range []string{"Samantha Doctor", "WARN", "models ensure"} {
		if !strings.Contains(output, want) {
			t.Errorf("doctor output should contain %q, got: %s", want, output)
		}
	}
}
