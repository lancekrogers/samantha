package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

func TestPersonaSystemPromptDefaultMatchesEmbeddedPrompt(t *testing.T) {
	cfg := &config.Config{AgentName: "TestAgent"}

	got, err := personaSystemPrompt(cfg)
	if err != nil {
		t.Fatalf("personaSystemPrompt() error = %v", err)
	}

	doc, err := prompts.Default(prompts.KindPersona)
	if err != nil {
		t.Fatalf("Default(persona) error = %v", err)
	}
	want, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": cfg.AgentName})
	if err != nil {
		t.Fatalf("ResolvePlaceholders() error = %v", err)
	}
	if got != want {
		t.Errorf("personaSystemPrompt() diverged from embedded default")
	}
}

func TestPersonaSystemPromptFailsForInvalidConfiguredPersona(t *testing.T) {
	dir := t.TempDir()
	personaDir := filepath.Join(dir, "persona")
	if err := os.MkdirAll(personaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(personaDir, "broken.yaml")
	if err := os.WriteFile(path, []byte(`schema: samantha.prompt.v1
prompt:
  name: broken
  kind: persona
  system_prompt: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := personaSystemPrompt(&config.Config{
		AgentName:  "Samantha",
		Persona:    "broken",
		PromptsDir: dir,
	})
	if err == nil {
		t.Fatal("personaSystemPrompt() error = nil, want invalid prompt document error")
	}
	for _, want := range []string{"resolving persona prompt", "broken", path, "system_prompt missing identity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestTurnInstructionUsesSharedDefaultWhenTurnPromptEmpty(t *testing.T) {
	// Custom personas leave prompts.turn empty; the brain must still get the
	// shared embedded turn instruction without resolving kind=turn under the
	// persona system-prompt name (which would 404 after wrong-name isolation).
	cfg := &config.Config{AgentName: "Uncle Fu", Persona: "uncle-fu", TurnPrompt: ""}
	got, err := turnInstruction(cfg)
	if err != nil {
		t.Fatalf("turnInstruction() error = %v", err)
	}
	doc, err := prompts.Default(prompts.KindTurn)
	if err != nil {
		t.Fatal(err)
	}
	want, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": "Uncle Fu"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("turnInstruction() = %q, want embedded default with agent name", got)
	}
}

func TestPersonaSystemPromptDoesNotFallBackToSamantha(t *testing.T) {
	dir := t.TempDir()
	_, err := personaSystemPrompt(&config.Config{
		AgentName:  "Uncle Fu",
		Persona:    "uncle-fu",
		PromptsDir: dir,
	})
	if err == nil {
		t.Fatal("personaSystemPrompt(uncle-fu) succeeded, want missing-document error (not silent samantha)")
	}
	if !strings.Contains(err.Error(), "uncle-fu") {
		t.Fatalf("error = %q, want uncle-fu named", err)
	}
}
