package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

func TestWriteAndLoadSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	// PromptsDir defaults to configDir/prompts.
	text := "You are {agent_name}, a terse research assistant."
	if err := WriteSystemPrompt("research", text); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "prompts", "persona", "research.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "research") || !strings.Contains(string(raw), "terse research") {
		t.Fatalf("file contents: %s", raw)
	}
	doc, err := prompts.LoadFile(path, prompts.KindPersona)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Prompt.Name != "research" {
		t.Fatalf("name = %q", doc.Prompt.Name)
	}
	got, err := LoadSystemPrompt("research")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "terse research") {
		t.Fatalf("LoadSystemPrompt = %q", got)
	}
}

func TestCreateWithOptsWritesPrompt(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := Write(&Profile{
		Schema: Schema, ID: "samantha", DisplayName: "Samantha", Builtin: true,
		TTS: TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart", Persona: "samantha"}
	p, err := CreateWithOpts(cfg, CreateOpts{
		DisplayName:  "Research Buddy",
		SystemPrompt: "You are {agent_name}. You love citations.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Prompts.Persona != p.ID {
		t.Fatalf("prompt ref = %q, want %q", p.Prompts.Persona, p.ID)
	}
	got, err := LoadSystemPrompt(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "love citations") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestUpdateSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := Write(&Profile{
		Schema: Schema, ID: "custom", DisplayName: "Custom",
		Prompts: PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}
	p, err := UpdateSystemPrompt("custom", "You are {agent_name}, updated.")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prompts.Persona != "custom" {
		t.Fatalf("ref = %q", p.Prompts.Persona)
	}
	got, _ := LoadSystemPrompt("custom")
	if !strings.Contains(got, "updated") {
		t.Fatalf("got %q", got)
	}
}
