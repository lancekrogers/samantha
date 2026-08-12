package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
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
	if p.Prompts.Turn != "" {
		t.Fatalf("turn ref = %q, want empty (shared embedded default)", p.Prompts.Turn)
	}
	got, err := LoadSystemPrompt(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "love citations") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestCreateAlwaysOwnsPrivatePrompt(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)
	cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart", Persona: "samantha"}
	p, err := Create(cfg, "Uncle Fu")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "uncle-fu" {
		t.Fatalf("id = %q", p.ID)
	}
	if p.Prompts.Persona != "uncle-fu" {
		t.Fatalf("prompts.persona = %q, want uncle-fu (not samantha)", p.Prompts.Persona)
	}
	if p.Prompts.Turn != "" {
		t.Fatalf("prompts.turn = %q, want empty", p.Prompts.Turn)
	}
	path := filepath.Join(dir, "prompts", "persona", "uncle-fu.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("private prompt missing: %v", err)
	}
	got, err := LoadSystemPrompt("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("seeded prompt empty")
	}
}

func TestLoadSystemPromptForProfileHealsStaleRef(t *testing.T) {
	// Real user bug: profile had prompts.persona: Uncle_Fu (TTS voice id) while
	// the private doc lived at prompts/persona/uncle-fu.yaml. Resolver used to
	// return the embedded samantha identity for the missing Uncle_Fu name.
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := WriteSystemPrompt("uncle-fu", "You are Uncle Fu, a private agent."); err != nil {
		t.Fatal(err)
	}
	if err := Write(&Profile{
		Schema: Schema, ID: "uncle-fu", DisplayName: "uncle fu",
		TTS:     TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu"},
		Prompts: PromptRefs{Persona: "Uncle_Fu", Turn: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}

	p, err := Load("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadSystemPromptForProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Uncle Fu, a private agent") {
		t.Fatalf("got %q, want private prompt (not samantha)", got)
	}
	// Healed on disk.
	healed, err := Load("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	if healed.Prompts.Persona != "uncle-fu" {
		t.Fatalf("prompts.persona = %q after heal, want uncle-fu", healed.Prompts.Persona)
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

func TestApplyPrefersPrivatePromptOverStaleRef(t *testing.T) {
	// The stale-ref profile the editor heals must not hard-fail the brain before
	// anyone opens the editor: Apply resolves cfg.Persona to the id whose private
	// document exists, so resolvePrompt finds a document.
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := WriteSystemPrompt("uncle-fu", "You are Uncle Fu, a private agent."); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "uncle-fu", DisplayName: "uncle fu",
		TTS:     TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu"},
		Prompts: PromptRefs{Persona: "Uncle_Fu"},
	})
	if cfg.Persona != "uncle-fu" {
		t.Fatalf("cfg.Persona = %q, want uncle-fu", cfg.Persona)
	}
	if _, err := (prompts.Resolver{UserDir: promptsDir()}).Resolve(prompts.KindPersona, cfg.Persona); err != nil {
		t.Fatalf("brain would fail to construct: %v", err)
	}
}

func TestApplyKeepsRefWhenNoPrivatePrompt(t *testing.T) {
	// No private document: keep the profile's ref so a genuinely missing prompt
	// still surfaces as an error instead of being silently rewritten.
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := WriteSystemPrompt("shared-base", "You are a shared base."); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	Apply(cfg, &Profile{
		Schema: Schema, ID: "borrower", DisplayName: "Borrower",
		Prompts: PromptRefs{Persona: "shared-base"},
	})
	if cfg.Persona != "shared-base" {
		t.Fatalf("cfg.Persona = %q, want shared-base", cfg.Persona)
	}
}

func TestApplyDoesNotOverrideSharedRefWithEmbeddedName(t *testing.T) {
	// The heal must key on a real user document, not the embedded fallback:
	// LoadSystemPrompt("samantha") always succeeds, so keying on it would rewrite
	// a deliberate shared ref on the samantha profile back to "samantha".
	dir := t.TempDir()
	setConfigDir(t, dir)
	if err := WriteSystemPrompt("team-voice", "You are the team voice."); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	Apply(cfg, &Profile{
		Schema: Schema, ID: DefaultID, DisplayName: "Samantha",
		Prompts: PromptRefs{Persona: "team-voice"},
	})
	if cfg.Persona != "team-voice" {
		t.Fatalf("cfg.Persona = %q, want the explicit shared ref kept", cfg.Persona)
	}
}
