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
		TTS:         TTS{Voice: "af_bella"},
		Prompts:     PromptRefs{Persona: "festival", Turn: "festival"},
	}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}

	got, err := Load("festival")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Festival" || got.TTS.Voice != "af_bella" {
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
		TTSVoice:      "af_heart",
		ActivePersona: "festival",
	}
	Apply(cfg, got)
	if cfg.AgentName != "Festival" || cfg.Persona != "festival" || cfg.TTSVoice != "af_bella" {
		t.Fatalf("Apply overlay failed: %+v", cfg)
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
