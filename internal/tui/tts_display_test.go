package tui

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
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

	// Empty models dir: no native package → not-installed native label.
	kokoroCfg := &config.Config{TTSProvider: "kokoro", ModelsDir: t.TempDir()}
	if got := ttsModelLabelForProvider("qwen3-tts", kokoroCfg); got != "native package (not installed)" {
		t.Fatalf("unselected Qwen model label = %q, want not-installed native package", got)
	}
	if detail := ttsProviderDetail(tts.ProviderSpec{Name: "qwen3-tts"}, kokoroCfg); !strings.Contains(detail, "native package (not installed)") || !strings.Contains(detail, "native worker (not installed)") {
		t.Fatalf("unselected Qwen provider detail = %q, want native package copy", detail)
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

func TestTTSDisplayManagedQwenUncleFuVoice(t *testing.T) {
	// Product selection (empty binary/model) with a persona voice like Uncle_Fu.
	// Isolate ModelsDir so a local native package does not flip the label.
	cfg := &config.Config{
		TTSProvider:  "qwen3-tts",
		QwenTTSVoice: "Uncle_Fu",
		ModelsDir:    t.TempDir(),
	}
	got := ttsBadgeLabel(cfg)
	want := "tts qwen3-tts · native package (not installed) · mode customvoice · voice Uncle_Fu"
	if got != want {
		t.Fatalf("ttsBadgeLabel() = %q, want %q", got, want)
	}
}

func TestTTSDisplayNativePackageLabel(t *testing.T) {
	dir := t.TempDir()
	writeNativeTUITestInstall(t, dir)

	cfg := &config.Config{
		TTSProvider: "qwen3-tts", ModelsDir: dir, QwenTTSModelTier: "0.6b",
		QwenTTSVoice: "Vivian",
	}
	got := ttsBadgeLabel(cfg)
	if !strings.Contains(got, "native 0.6b") || !strings.Contains(got, "Vivian") {
		t.Fatalf("native badge = %q", got)
	}
}
