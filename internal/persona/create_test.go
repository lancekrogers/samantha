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
	if _, err := os.Stat(filepath.Join(dir, "personas", "research-buddy", "persona.yaml")); err != nil {
		t.Fatalf("profile missing: %v", err)
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
