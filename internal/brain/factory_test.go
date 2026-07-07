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
	for _, provider := range []string{"not-real", "claude", ""} {
		cfg := &config.Config{BrainProvider: provider}

		_, err := NewBatchProvider(cfg)
		if err == nil {
			t.Fatalf("NewBatchProvider(%q) error = nil, want unsupported provider error", provider)
		}
		if !strings.Contains(err.Error(), "unsupported batch brain_provider") {
			t.Fatalf("NewBatchProvider(%q) error = %q, want unsupported batch brain_provider message", provider, err)
		}
	}
}
