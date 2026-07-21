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
	if got := ttsBadgeLabel(cfg); got != "tts kokoro · managed · mode static · voice af_heart" {
		t.Fatalf("ttsBadgeLabel() = %q, want managed Kokoro badge", got)
	}
}

func TestTTSDisplayIdentifiesQwenModelAndBinary(t *testing.T) {
	cfg := &config.Config{
		TTSProvider:   "qwen3-tts",
		QwenTTSModel:  "/opt/qwen/models/1.7b",
		QwenTTSBinary: "/opt/qwen/bin/qwen3-tts-cli",
	}

	if got := ttsBadgeLabel(cfg); got != "tts qwen3-tts · 1.7b · mode unverified/default · voice model-native default" {
		t.Fatalf("ttsBadgeLabel() = %q, want Qwen model badge", got)
	}
	detail := ttsProviderDetail(tts.ProviderSpec{Name: "qwen3-tts"}, cfg)
	if !strings.Contains(detail, "model 1.7b") || !strings.Contains(detail, "qwen3-tts-cli") {
		t.Fatalf("Qwen provider detail = %q, want model and binary", detail)
	}

	kokoroCfg := &config.Config{TTSProvider: "kokoro"}
	if got := ttsModelLabelForProvider("qwen3-tts", kokoroCfg); got != "unset" {
		t.Fatalf("unselected Qwen model label = %q, want unset", got)
	}
	if detail := ttsProviderDetail(tts.ProviderSpec{Name: "qwen3-tts"}, kokoroCfg); !strings.Contains(detail, "model unset") || strings.Contains(detail, "model model unset") {
		t.Fatalf("unselected Qwen provider detail = %q, want sensible unset model copy", detail)
	}
}

func TestTTSDisplayExplainsUnverifiedQwenVoiceModes(t *testing.T) {
	cfg := &config.Config{TTSProvider: "qwen3-tts", QwenTTSModel: "/models/customvoice"}
	if got := ttsVoiceModeLabel(cfg); got != "mode unverified/default" {
		t.Fatalf("Qwen mode label = %q, want unverified/default", got)
	}
	if got := ttsEffectiveVoiceLabel(cfg); got != "voice model-native default" {
		t.Fatalf("Qwen voice label = %q, want model-native default", got)
	}
	if got := ttsVoiceSelectionStatus(cfg); !strings.Contains(got, "not verified") {
		t.Fatalf("Qwen voice status = %q, want actionable unverified explanation", got)
	}
}
