package persona

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Agent":     "my-agent",
		"  Festival  ": "festival",
		"Obey_Voice":   "obey-voice",
		"A/B Test":     "a-b-test",
		"!!!":          "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateAndUse(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	// Seed an existing samantha so Create can run against a normal install.
	if err := Write(&Profile{
		Schema: Schema, ID: "samantha", DisplayName: "Samantha", Builtin: true,
		TTS:     TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: PromptRefs{Persona: "samantha", Turn: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ActivePersona: "samantha",
		AgentName:     "Samantha",
		TTSProvider:   "kokoro",
		TTSVoice:      "af_sky",
		Persona:       "samantha",
	}
	p, err := CreateAndUse(cfg, "Research Buddy")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "research-buddy" {
		t.Fatalf("id = %q, want research-buddy", p.ID)
	}
	if p.Builtin {
		t.Fatal("user-created persona must not be builtin")
	}
	if p.TTS.Voice != "af_sky" || p.TTS.Provider != "kokoro" {
		t.Fatalf("cloned TTS = %+v", p.TTS)
	}
	if cfg.ActivePersona != "research-buddy" || cfg.AgentName != "Research Buddy" {
		t.Fatalf("cfg after CreateAndUse = active=%q name=%q", cfg.ActivePersona, cfg.AgentName)
	}
	if p.Prompts.Persona != "research-buddy" {
		t.Fatalf("prompts.persona = %q, want research-buddy", p.Prompts.Persona)
	}
	if p.Prompts.Turn != "" {
		t.Fatalf("prompts.turn = %q, want empty shared default", p.Prompts.Turn)
	}
	if _, err := os.Stat(filepath.Join(dir, "personas", "research-buddy", "persona.yaml")); err != nil {
		t.Fatalf("profile missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompts", "persona", "research-buddy.yaml")); err != nil {
		t.Fatalf("private system prompt missing: %v", err)
	}

	// Second create with same name gets a suffix.
	p2, err := Create(cfg, "Research Buddy")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != "research-buddy-2" {
		t.Fatalf("second id = %q, want research-buddy-2", p2.ID)
	}
}

func TestCreateRequiresName(t *testing.T) {
	if _, err := Create(&config.Config{}, "  "); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCreateClonesGlobalBrainStack(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "ollama", OllamaModel: "llama3", TTSProvider: "kokoro", TTSVoice: "af_heart"}

	p, err := Create(cfg, "Clone Kid")
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.Provider != "ollama" || p.Brain.Model != "llama3" {
		t.Fatalf("created brain = %+v, want globals cloned at create time", p.Brain)
	}
}

func TestCreateWithOptsExplicitStack(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "claude", TTSProvider: "kokoro", TTSVoice: "af_heart"}

	p, err := CreateWithOpts(cfg, CreateOpts{
		DisplayName: "Local Sage",
		Brain:       Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS:         TTS{Provider: "qwen3-tts", Voice: "Ryan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Brain.Provider != "ollama" || got.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("persisted brain = %+v", got.Brain)
	}
	if got.TTS.Provider != "qwen3-tts" || got.TTS.Voice != "Ryan" {
		t.Fatalf("persisted tts = %+v", got.TTS)
	}
}
