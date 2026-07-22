package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
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
