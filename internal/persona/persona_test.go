package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	config.SetConfigDirForTest(t, dir)
}

func TestValidateID(t *testing.T) {
	for _, id := range []string{"samantha", "festival", "obey", "my-agent", "a1"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "Samantha", "has space", "under_score", "-lead", "trail-", "a--b"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestWriteLoadListApply(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	p := &Profile{
		Schema:      Schema,
		ID:          "festival",
		DisplayName: "Festival",
		Builtin:     true,
		TTS:         TTS{Provider: "kokoro", Voice: "af_bella"},
		Prompts:     PromptRefs{Persona: "festival", Turn: "festival"},
	}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}

	got, err := Load("festival")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Festival" || got.TTS.Voice != "af_bella" || got.TTS.Provider != "kokoro" {
		t.Fatalf("Load() = %+v", got)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "festival" {
		t.Fatalf("List() = %+v", list)
	}

	cfg := &config.Config{
		AgentName:     "Old",
		Persona:       "samantha",
		TTSProvider:   "kokoro",
		TTSVoice:      "af_heart",
		ActivePersona: "festival",
	}
	Apply(cfg, got)
	if cfg.AgentName != "Festival" || cfg.Persona != "festival" || cfg.TTSVoice != "af_bella" || cfg.TTSProvider != "kokoro" {
		t.Fatalf("Apply overlay failed: %+v", cfg)
	}
}

func TestApplyQwenProviderAndVoice(t *testing.T) {
	cfg := &config.Config{
		TTSProvider:  "kokoro",
		TTSVoice:     "af_heart",
		QwenTTSVoice: "",
	}
	Apply(cfg, &Profile{
		ID:          "obey",
		DisplayName: "Obey",
		Prompts:     PromptRefs{Persona: "obey"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Vivian"},
	})
	if cfg.TTSProvider != "qwen3-tts" {
		t.Fatalf("TTSProvider = %q, want qwen3-tts", cfg.TTSProvider)
	}
	if cfg.QwenTTSVoice != "Vivian" {
		t.Fatalf("QwenTTSVoice = %q, want Vivian", cfg.QwenTTSVoice)
	}
	// Kokoro voice left as-is (not cleared) — factory uses provider to pick keys.
	if cfg.TTSVoice != "af_heart" {
		t.Fatalf("TTSVoice = %q, want af_heart unchanged", cfg.TTSVoice)
	}
}

func TestApplyQwenTier(t *testing.T) {
	// A qwen persona pins its own native model tier; spelling variants
	// normalize to the canonical tier name.
	cfg := &config.Config{TTSProvider: "kokoro", QwenTTSModelTier: "0.6b"}
	Apply(cfg, &Profile{
		ID:          "veronica",
		DisplayName: "Veronica",
		Prompts:     PromptRefs{Persona: "veronica"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Ono_Anna", Tier: "1.7"},
	})
	if cfg.QwenTTSModelTier != "1.7b" {
		t.Fatalf("QwenTTSModelTier = %q, want 1.7b", cfg.QwenTTSModelTier)
	}

	// Empty tier inherits the app-level setting.
	cfg = &config.Config{QwenTTSModelTier: "1.7b"}
	Apply(cfg, &Profile{
		ID:          "dylan",
		DisplayName: "Dylan",
		Prompts:     PromptRefs{Persona: "dylan"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Vivian"},
	})
	if cfg.QwenTTSModelTier != "1.7b" {
		t.Fatalf("QwenTTSModelTier = %q, want inherited 1.7b", cfg.QwenTTSModelTier)
	}

	// A non-qwen persona never touches the tier, even if one is recorded.
	cfg = &config.Config{QwenTTSModelTier: "0.6b"}
	Apply(cfg, &Profile{
		ID:          "samantha",
		DisplayName: "Samantha",
		Prompts:     PromptRefs{Persona: "samantha"},
		TTS:         TTS{Provider: "kokoro", Voice: "af_sky", Tier: "1.7b"},
	})
	if cfg.QwenTTSModelTier != "0.6b" {
		t.Fatalf("QwenTTSModelTier = %q, want untouched 0.6b", cfg.QwenTTSModelTier)
	}
}

func TestValidateRejectsUnknownTier(t *testing.T) {
	p := &Profile{
		Schema: Schema, ID: "x", DisplayName: "X",
		Prompts: PromptRefs{Persona: "x"},
		TTS:     TTS{Provider: "qwen3-tts", Tier: "3b"},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate() = nil, want unknown-tier error")
	}
	p.TTS.Tier = "1.7b"
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for known tier", err)
	}
}

func TestFromConfigCapturesQwenVoice(t *testing.T) {
	p := FromConfig(&config.Config{
		AgentName:    "Q",
		Persona:      "samantha",
		TTSProvider:  "qwen3-tts",
		QwenTTSVoice: "Ryan",
		TTSVoice:     "af_heart",
	})
	if p.TTS.Provider != "qwen3-tts" || p.TTS.Voice != "Ryan" {
		t.Fatalf("FromConfig TTS = %+v", p.TTS)
	}
}

func TestUpdateStackPersistsBrainAndTTS(t *testing.T) {
	// UpdateStack replaces UpdateActiveTTS as the only write path for a
	// persona's model/voice stack (WI-c8884d §5.2): it targets one profile
	// by id and never touches live config or global keys.
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := Write(&Profile{
		Schema: Schema, ID: "samantha", DisplayName: "Samantha",
		TTS:     TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	p, err := UpdateStack("samantha",
		Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS{Provider: "qwen3-tts", Voice: "Ryan", Tier: "1.7b"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.Provider != "ollama" || p.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("returned brain = %+v", p.Brain)
	}

	profile, err := Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Brain.Provider != "ollama" || profile.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("persisted brain = %+v, want ollama/qwen2.5:14b", profile.Brain)
	}
	if profile.TTS.Provider != "qwen3-tts" || profile.TTS.Voice != "Ryan" || profile.TTS.Tier != "1.7b" {
		t.Fatalf("persisted TTS = %+v, want qwen3-tts/Ryan/1.7b", profile.TTS)
	}

	// Empty fields clear back to inherit-global.
	if _, err := UpdateStack("samantha", Brain{}, TTS{}); err != nil {
		t.Fatal(err)
	}
	profile, err = Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Brain.Provider != "" || profile.TTS.Provider != "" {
		t.Fatalf("cleared stack = brain %+v tts %+v, want inherit-global", profile.Brain, profile.TTS)
	}
}

func TestApplyRoutesPersonaBrainModel(t *testing.T) {
	cfg := &config.Config{BrainProvider: "claude", OllamaModel: "llama3"}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "research", DisplayName: "Research",
		Brain:   Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		Prompts: PromptRefs{Persona: "research"},
	})
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "qwen2.5:14b" {
		t.Fatalf("cfg = provider %q model %q, want ollama/qwen2.5:14b", cfg.BrainProvider, cfg.OllamaModel)
	}

	// Empty brain fields inherit the app defaults untouched.
	cfg = &config.Config{BrainProvider: "ollama", OllamaModel: "llama3"}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "plain", DisplayName: "Plain",
		Prompts: PromptRefs{Persona: "plain"},
	})
	if cfg.BrainProvider != "ollama" || cfg.OllamaModel != "llama3" {
		t.Fatalf("cfg mutated by empty brain: provider %q model %q", cfg.BrainProvider, cfg.OllamaModel)
	}
}

func TestEnsureAndApplyMigratesFromLegacy(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	cfg := &config.Config{
		AgentName: "LegacySam",
		Persona:   "samantha",
		TTSVoice:  "af_nova",
	}
	if err := EnsureAndApply(cfg); err != nil {
		t.Fatal(err)
	}

	path := ProfilePath("samantha")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected migrated profile at %s: %v", path, err)
	}
	if cfg.AgentName != "LegacySam" {
		t.Errorf("AgentName = %q, want LegacySam", cfg.AgentName)
	}
	if cfg.TTSVoice != "af_nova" {
		t.Errorf("TTSVoice = %q, want af_nova", cfg.TTSVoice)
	}
	if cfg.ActivePersona != "samantha" {
		t.Errorf("ActivePersona = %q, want samantha", cfg.ActivePersona)
	}

	// Second call is idempotent and does not clobber.
	raw, _ := os.ReadFile(path)
	if err := EnsureAndApply(cfg); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path)
	if string(raw) != string(raw2) {
		t.Fatal("EnsureAndApply rewrote existing profile")
	}
}

// Regression: viper defaults active_persona to "samantha" even when the legacy
// persona prompt name is a different slug. Migration must create that slug's
// profile and set active_persona to match (not leave active pointing at a
// missing samantha profile).
func TestEnsureAndApplyMigratesLegacyNonDefaultPersona(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	cfg := &config.Config{
		AgentName:     "Festival Bot",
		Persona:       "festival",
		TTSVoice:      "af_bella",
		ActivePersona: "samantha", // viper default; not a real profile yet
	}
	if err := EnsureAndApply(cfg); err != nil {
		t.Fatalf("EnsureAndApply() error = %v", err)
	}

	if _, err := os.Stat(ProfilePath("festival")); err != nil {
		t.Fatalf("expected migrated profile at festival: %v", err)
	}
	if _, err := os.Stat(ProfilePath("samantha")); err == nil {
		t.Fatal("did not expect a samantha profile for legacy persona=festival")
	}
	if cfg.ActivePersona != "festival" {
		t.Errorf("ActivePersona = %q, want festival", cfg.ActivePersona)
	}
	if cfg.AgentName != "Festival Bot" || cfg.TTSVoice != "af_bella" || cfg.Persona != "festival" {
		t.Fatalf("overlay mismatch: name=%q voice=%q persona=%q", cfg.AgentName, cfg.TTSVoice, cfg.Persona)
	}

	// Second load still heals if active stays at the viper default.
	cfg2 := &config.Config{
		AgentName:     "Festival Bot",
		Persona:       "festival",
		TTSVoice:      "af_bella",
		ActivePersona: "samantha",
	}
	if err := EnsureAndApply(cfg2); err != nil {
		t.Fatalf("second EnsureAndApply() error = %v", err)
	}
	if cfg2.ActivePersona != "festival" {
		t.Errorf("second ActivePersona = %q, want festival", cfg2.ActivePersona)
	}
}

func TestEnsureAndApplyMissingActive(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	// Seed one profile, then request a missing id.
	if err := Write(&Profile{
		Schema:      Schema,
		ID:          "samantha",
		DisplayName: "Samantha",
		Builtin:     true,
		Prompts:     PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{ActivePersona: "nope"}
	err := EnsureAndApply(cfg)
	if err == nil {
		t.Fatal("expected error for missing active_persona")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileIDMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "samantha", "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`schema: festival-voice.persona.v1
id: festival
display_name: X
prompts:
  persona: festival
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "does not match directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestFromConfig(t *testing.T) {
	p := FromConfig(&config.Config{
		AgentName: "A",
		Persona:   "samantha",
		TTSVoice:  "v",
	})
	if p.ID != "samantha" || p.DisplayName != "A" || p.TTS.Voice != "v" {
		t.Fatalf("%+v", p)
	}
}
