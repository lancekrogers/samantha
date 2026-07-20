package tui

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/tts"
)

func TestTTSDisplayDefaultsToManagedKokoro(t *testing.T) {
	cfg := &config.Config{}

	if got := activeTTSProvider(cfg); got != "kokoro" {
		t.Fatalf("activeTTSProvider() = %q, want kokoro", got)
	}
	if got := ttsBadgeLabel(cfg); got != "tts kokoro · managed" {
		t.Fatalf("ttsBadgeLabel() = %q, want managed Kokoro badge", got)
	}
}

func TestTTSDisplayIdentifiesQwenModelAndBinary(t *testing.T) {
	cfg := &config.Config{
		TTSProvider:   "qwen3-tts",
		QwenTTSModel:  "/opt/qwen/models/1.7b",
		QwenTTSBinary: "/opt/qwen/bin/qwen3-tts-cli",
	}

	if got := ttsBadgeLabel(cfg); got != "tts qwen3-tts · 1.7b" {
		t.Fatalf("ttsBadgeLabel() = %q, want Qwen model badge", got)
	}
	detail := ttsProviderDetail(tts.ProviderSpec{Name: "qwen3-tts"}, cfg)
	if !strings.Contains(detail, "model 1.7b") || !strings.Contains(detail, "qwen3-tts-cli") {
		t.Fatalf("Qwen provider detail = %q, want model and binary", detail)
	}

	kokoroCfg := &config.Config{TTSProvider: "kokoro"}
	if detail := ttsProviderDetail(tts.ProviderSpec{Name: "qwen3-tts"}, kokoroCfg); !strings.Contains(detail, "model model unset") {
		t.Fatalf("unselected Qwen provider detail = %q, want unset model", detail)
	}
}
