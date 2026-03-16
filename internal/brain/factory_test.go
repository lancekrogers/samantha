package brain

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/samantha/internal/config"
)

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
