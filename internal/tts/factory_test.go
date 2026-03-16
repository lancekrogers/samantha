package tts

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/samantha/internal/config"
)

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{TTSProvider: "edge"}

	_, _, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported tts_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported tts_provider message", err)
	}
}

func TestStaticVoicesForKokoro(t *testing.T) {
	voices, err := StaticVoices("kokoro", "en-US", "female")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	if len(voices) == 0 {
		t.Fatal("StaticVoices() returned no voices, want at least one")
	}
}
