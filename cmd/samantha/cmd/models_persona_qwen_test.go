package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func writeQwenPersona(t *testing.T, id, tier string) {
	t.Helper()
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: id, DisplayName: id,
		TTS:     persona.TTS{Provider: "qwen3-tts", Voice: "Vivian", Tier: tier},
		Prompts: persona.PromptRefs{Persona: id},
	}, false); err != nil {
		t.Fatal(err)
	}
}

// Personas route speech independently of the active provider: ensure --tts on
// a kokoro config still installs the tiers qwen personas need.
func TestEnsureCoversQwenPersonaTiers(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeQwenPersona(t, "veronica", "1.7b")
	writeQwenPersona(t, "dylan", "")

	var tiers []string
	orig := ensureQwenTierFn
	ensureQwenTierFn = func(_ context.Context, _ *config.Config, tier string, _ func(string, float64)) error {
		tiers = append(tiers, tier)
		return nil
	}
	t.Cleanup(func() { ensureQwenTierFn = orig })

	cfg := &config.Config{TTSProvider: "kokoro", QwenTTSModelTier: "0.6b", ModelsDir: t.TempDir()}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	fake := func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error { return nil }

	if err := runModelsEnsure(cmd, cfg, scopeFlags{tts: true}.request(cfg), fake); err != nil {
		t.Fatalf("runModelsEnsure() error = %v", err)
	}
	if len(tiers) != 2 || tiers[0] != "0.6b" || tiers[1] != "1.7b" {
		t.Fatalf("ensured tiers = %v, want [0.6b 1.7b] (dylan inherits global, veronica pins 1.7b)", tiers)
	}

	// Non-TTS scopes never touch the qwen package.
	tiers = nil
	if err := runModelsEnsure(cmd, cfg, scopeFlags{stt: true}.request(cfg), fake); err != nil {
		t.Fatalf("runModelsEnsure(stt) error = %v", err)
	}
	if len(tiers) != 0 {
		t.Fatalf("ensured tiers = %v for stt scope, want none", tiers)
	}
}

// Doctor names the persona and tier when the installed package cannot speak it.
func TestDoctorWarnsOnUnsatisfiedQwenPersona(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeQwenPersona(t, "veronica", "1.7b")

	diags := personaQwenDiags(&config.Config{QwenTTSModelTier: "0.6b"}, t.TempDir())
	if len(diags) != 1 {
		t.Fatalf("diags = %+v, want one warning", diags)
	}
	d := diags[0]
	if d.Severity != config.SeverityWarn || !strings.Contains(d.Detail, "veronica") || !strings.Contains(d.Detail, "1.7b") {
		t.Fatalf("diag = %+v, want warn naming veronica and 1.7b", d)
	}
}
