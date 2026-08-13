package brain

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func TestProvidersIncludesGrok(t *testing.T) {
	var spec ProviderSpec
	for _, p := range Providers() {
		if p.Name == "grok" {
			spec = p
			break
		}
	}
	if spec.Name != "grok" {
		t.Fatalf("Providers() missing grok provider, got %+v", Providers())
	}
	if spec.Description == "" {
		t.Error("grok provider spec has empty description")
	}
}

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{BrainProvider: "not-real"}

	_, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported brain_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported brain_provider message", err)
	}
}

func TestNewBatchProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{BrainProvider: "not-real"}

	_, err := NewBatchProvider(cfg)
	if err == nil {
		t.Fatal("NewBatchProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported batch brain_provider") {
		t.Fatalf("NewBatchProvider() error = %q, want unsupported batch brain_provider message", err)
	}
}

// The default config leaves brain_provider empty and defaults to Ollama, so the
// batch factory must resolve every implemented name — plus "" — to a real
// adapter and never to an unsupported-provider error. Construction touches the
// environment (ollama server/model, claude CLI on PATH), so tolerate those
// setup errors and assert only on the routing.
func TestNewBatchProviderAcceptsImplementedProviders(t *testing.T) {
	tolerated := []string{
		"ollama_model not configured", // default config, no model chosen yet
		"cannot connect to ollama",    // no local server in CI
		"not found in ollama",         // server up, model not pulled
		"claude CLI not found",
		"grok CLI not found",
	}
	for _, provider := range []string{"", "ollama", "claude", "grok"} {
		cfg := &config.Config{BrainProvider: provider}

		_, err := NewBatchProvider(cfg)
		if err == nil {
			continue
		}
		setupErr := false
		for _, want := range tolerated {
			if strings.Contains(err.Error(), want) {
				setupErr = true
				break
			}
		}
		if !setupErr {
			t.Fatalf("NewBatchProvider(%q) error = %q, want success or an environment setup error (never unsupported provider)", provider, err)
		}
	}
}
