package stt

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/samantha/internal/config"
)

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{STTProvider: "google"}

	_, _, err := NewProvider(cfg, nil, nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported stt_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported stt_provider message", err)
	}
}

func TestNewProviderRequiresVADForSherpa(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa"}

	_, _, err := NewProvider(cfg, nil, nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want missing dependency error")
	}
	if !strings.Contains(err.Error(), "requires audio capture") {
		t.Fatalf("NewProvider() error = %q, want capture requirement message", err)
	}
}
