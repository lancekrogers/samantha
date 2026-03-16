//go:build integration
// +build integration

package integration

import (
	"strings"
	"testing"
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
	_, err := tc.RunSamantha("config", "set", "tts_voice", "af_bella")
	if err != nil {
		t.Fatalf("samantha config set failed: %v", err)
	}

	// Verify the config file was written.
	content, err := tc.ReadFile("/root/.obey/agents/voice/samantha/config.yaml")
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
