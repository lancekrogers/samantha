package tts

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
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

func TestProvidersIncludesOptionalQwen(t *testing.T) {
	for _, spec := range Providers() {
		if spec.Name == "qwen3-tts" {
			return
		}
	}
	t.Fatalf("Providers() = %+v, missing qwen3-tts", Providers())
}

func TestStaticVoicesForQwenIsDynamic(t *testing.T) {
	voices, err := StaticVoices("qwen3-tts", "", "")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	if len(voices) != 0 {
		t.Fatalf("StaticVoices() = %+v, want no static voices", voices)
	}
}
