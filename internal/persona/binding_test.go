package persona

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func writeProfile(t *testing.T, id, name, promptRef string) {
	t.Helper()
	p := &Profile{
		Schema:      Schema,
		ID:          id,
		DisplayName: name,
		TTS:         TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts:     PromptRefs{Persona: promptRef},
	}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBindingSnapshotsIdentity(t *testing.T) {
	setConfigDir(t, t.TempDir())
	writeProfile(t, "ada", "Ada", "ada-prompt")
	writeProfile(t, "bob", "Bob", "bob-prompt")
	cfg := &config.Config{ActivePersona: "ada", AgentName: "Samantha"}

	binding, err := ResolveBinding(cfg, "")
	if err != nil {
		t.Fatalf("ResolveBinding() error = %v", err)
	}
	if binding.PersonaID != "ada" || binding.AgentName != "Ada" || binding.PromptRef != "ada-prompt" {
		t.Fatalf("binding = %+v, want ada identity", binding)
	}
	if binding.TTS.Voice != "af_heart" {
		t.Fatalf("binding TTS = %+v, want profile voice", binding.TTS)
	}
	// Resolving must not mutate the caller's config — Apply runs on the
	// snapshot only.
	if cfg.AgentName != "Samantha" {
		t.Fatalf("cfg.AgentName = %q, ResolveBinding mutated the caller's config", cfg.AgentName)
	}
}

func TestBindingIsolatedFromLaterPersonaEdits(t *testing.T) {
	setConfigDir(t, t.TempDir())
	writeProfile(t, "ada", "Ada", "ada-prompt")
	writeProfile(t, "bob", "Bob", "bob-prompt")
	cfg := &config.Config{ActivePersona: "ada"}

	binding, err := ResolveBinding(cfg, "")
	if err != nil {
		t.Fatalf("ResolveBinding() error = %v", err)
	}

	// Edit the bound persona on disk and switch the global active persona —
	// both happen while the session is "in flight".
	writeProfile(t, "ada", "Evil Ada", "evil-prompt")
	if err := Use(cfg, "bob"); err != nil {
		t.Fatalf("Use() error = %v", err)
	}

	if binding.AgentName != "Ada" || binding.PromptRef != "ada-prompt" {
		t.Fatalf("binding changed after disk edit/persona switch: %+v", binding)
	}
	bcfg := binding.Config()
	if bcfg.AgentName != "Ada" || bcfg.Persona != "ada-prompt" {
		t.Fatalf("binding config tracked live state: agent %q persona %q", bcfg.AgentName, bcfg.Persona)
	}

	// Use() still updated the default for the NEXT session.
	if cfg.ActivePersona != "bob" {
		t.Fatalf("ActivePersona = %q, want bob after Use", cfg.ActivePersona)
	}
	next, err := ResolveBinding(cfg, "")
	if err != nil {
		t.Fatalf("ResolveBinding() after Use error = %v", err)
	}
	if next.PersonaID != "bob" || next.AgentName != "Bob" {
		t.Fatalf("next binding = %+v, want bob identity", next)
	}
}

func TestResolveBindingExplicitIDOverridesActive(t *testing.T) {
	setConfigDir(t, t.TempDir())
	writeProfile(t, "ada", "Ada", "ada-prompt")
	writeProfile(t, "bob", "Bob", "bob-prompt")
	cfg := &config.Config{ActivePersona: "ada"}

	binding, err := ResolveBinding(cfg, "bob")
	if err != nil {
		t.Fatalf("ResolveBinding(bob) error = %v", err)
	}
	if binding.PersonaID != "bob" || binding.AgentName != "Bob" {
		t.Fatalf("binding = %+v, want bob identity", binding)
	}
	if cfg.ActivePersona != "ada" {
		t.Fatalf("explicit binding changed active persona to %q", cfg.ActivePersona)
	}
}

func TestBindingConfigReturnsIndependentCopies(t *testing.T) {
	setConfigDir(t, t.TempDir())
	writeProfile(t, "ada", "Ada", "ada-prompt")
	cfg := &config.Config{ActivePersona: "ada"}

	binding, err := ResolveBinding(cfg, "")
	if err != nil {
		t.Fatalf("ResolveBinding() error = %v", err)
	}
	c1 := binding.Config()
	c1.AgentName = "Mutated"
	if got := binding.Config().AgentName; got != "Ada" {
		t.Fatalf("binding snapshot mutated through Config() copy: %q", got)
	}
}

func TestResolveBindingUnknownID(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cfg := &config.Config{}
	if _, err := ResolveBinding(cfg, "ghost"); err == nil {
		t.Fatal("ResolveBinding(ghost) = nil error, want not-found")
	}
	if _, err := ResolveBinding(nil, "ada"); err == nil {
		t.Fatal("ResolveBinding(nil cfg) = nil error, want error")
	}
}
