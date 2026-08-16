package tui

import (
	"context"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

func qwenTierRowIndex(t *testing.T, m *settingsModel) int {
	t.Helper()
	for i, it := range m.qwenItems {
		if it.kind == qwenOptTier {
			return i
		}
	}
	t.Fatal("no tier row in qwen items")
	return -1
}

// Selecting 1.7b when the installed package lacks it must start the package
// install targeted at that tier — an install job, not a refusal.
func TestQwenTierRowInstallsMissingTier(t *testing.T) {
	saved := map[string]any{}
	var ensured *config.Config
	m := settingsModel{
		cfg:     &config.Config{QwenTTSModelTier: "0.6b", ModelsDir: t.TempDir()},
		section: sectionQwen,
		saveConfig: func(key string, value any) error {
			saved[key] = value
			return nil
		},
		ensureTTSAssets: func(_ context.Context, cfg *config.Config, _ func(string, float64)) error {
			ensured = cfg
			return nil
		},
	}
	m.buildQwenItems()
	m.cursor = qwenTierRowIndex(t, &m)

	cmd := m.selectQwenItem()
	if cmd == nil {
		t.Fatal("selectQwenItem() = nil cmd, want install batch")
	}
	if !m.qwenInstalling || m.qwenTierPending != managedqwen.Tier1_7B {
		t.Fatalf("installing=%v pending=%q, want install pending 1.7b", m.qwenInstalling, m.qwenTierPending)
	}
	if _, ok := saved["qwen_tts_model_tier"]; ok {
		t.Fatal("tier saved before the install landed")
	}

	// The install cmd must target the pending tier through the qwen provider.
	if msg := m.installQwenAssets(context.Background())(); msg == nil {
		t.Fatal("installQwenAssets returned nil msg")
	}
	if ensured == nil || ensured.QwenTTSModelTier != managedqwen.Tier1_7B || ensured.TTSProvider != managedqwen.ProviderName {
		t.Fatalf("ensure cfg = %+v, want qwen provider with tier 1.7b", ensured)
	}
}

// A successful pending-tier install saves the tier; the TTS provider is never
// flipped as a side effect of a tier upgrade.
func TestQwenTierInstallDoneSavesTier(t *testing.T) {
	saved := map[string]any{}
	m := settingsModel{
		cfg:             &config.Config{QwenTTSModelTier: "0.6b", TTSProvider: "kokoro", ModelsDir: t.TempDir()},
		qwenInstalling:  true,
		qwenTierPending: managedqwen.Tier1_7B,
		saveConfig: func(key string, value any) error {
			saved[key] = value
			return nil
		},
	}

	m2, _ := m.Update(qwenInstallDoneMsg{native: managedqwen.NativeStatus{Installed: true, ModelReady: true}})
	if got := saved["qwen_tts_model_tier"]; got != managedqwen.Tier1_7B {
		t.Fatalf("saved tier = %v, want 1.7b", got)
	}
	if m2.cfg.QwenTTSModelTier != managedqwen.Tier1_7B {
		t.Fatalf("cfg tier = %q, want 1.7b", m2.cfg.QwenTTSModelTier)
	}
	if _, ok := saved["tts_provider"]; ok || m2.cfg.TTSProvider != "kokoro" {
		t.Fatalf("tier upgrade must not flip the TTS provider (saved=%v provider=%q)", saved, m2.cfg.TTSProvider)
	}
	if m2.qwenTierPending != "" || m2.qwenInstalling {
		t.Fatalf("pending=%q installing=%v, want cleared", m2.qwenTierPending, m2.qwenInstalling)
	}
}

// A failed or tier-less install must not save the tier.
func TestQwenTierInstallDoneWithoutModelKeepsTier(t *testing.T) {
	saved := map[string]any{}
	m := settingsModel{
		cfg:             &config.Config{QwenTTSModelTier: "0.6b", ModelsDir: t.TempDir()},
		qwenInstalling:  true,
		qwenTierPending: managedqwen.Tier1_7B,
		saveConfig: func(key string, value any) error {
			saved[key] = value
			return nil
		},
	}
	m2, _ := m.Update(qwenInstallDoneMsg{native: managedqwen.NativeStatus{Installed: true, ModelReady: false}})
	if len(saved) != 0 {
		t.Fatalf("saved = %v, want nothing on tier-less install", saved)
	}
	if m2.cfg.QwenTTSModelTier != "0.6b" {
		t.Fatalf("cfg tier = %q, want unchanged 0.6b", m2.cfg.QwenTTSModelTier)
	}
}
