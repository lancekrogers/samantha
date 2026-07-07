package brain

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
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

// The default config leaves brain_provider empty and defaults to Claude, so the
// batch factory must resolve "" and "claude" to the Claude adapter — never an
// unsupported-provider error. Constructing it only requires the claude CLI on
// PATH, so tolerate that being absent (mirrors how NewProvider's claude case is
// left untested against a real CLI).
func TestNewBatchProviderAcceptsDefaultClaude(t *testing.T) {
	for _, provider := range []string{"", "claude"} {
		cfg := &config.Config{BrainProvider: provider}

		_, err := NewBatchProvider(cfg)
		if err != nil && !strings.Contains(err.Error(), "claude CLI not found") {
			t.Fatalf("NewBatchProvider(%q) error = %q, want success or claude-CLI-not-found (never unsupported provider)", provider, err)
		}
	}
}
