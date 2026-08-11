package brain

import (
	"slices"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/skills"
)

func policyCfg() *config.Config {
	return &config.Config{AgentName: "Samantha", EnvironmentContextEnabled: true}
}

// The defect this whole sequence exists to fix: the same persona behaved
// differently depending on which brain was selected, because ollama received
// environment grounding but no turn instruction while Claude and Grok received
// the reverse.
//
// Elements that define personality and grounding must be identical across
// providers. Elements that adapt to a provider's native capabilities must not
// be — and every such exception states its reason.
func TestSystemPromptPolicyIsIdenticalAcrossProviders(t *testing.T) {
	cfg := policyCfg()

	// Everything except the documented provider-adaptive element.
	shared := []PromptElement{ElemPersona, ElemEnvironment, ElemTurn}

	for _, provider := range []string{providerOllama, providerClaude, providerGrok} {
		got := SystemPromptPolicy(provider, cfg)

		var universal []PromptElement
		for _, e := range got {
			if e != ElemSkills {
				universal = append(universal, e)
			}
		}
		if !slices.Equal(universal, shared) {
			t.Errorf("provider %q: universal elements = %v, want %v\n"+
				"Personality and grounding elements must match across providers; "+
				"only provider-adaptive elements may differ.", provider, universal, shared)
		}
	}
}

// The one deliberate asymmetry. If this test fails because someone extended the
// skills menu to Claude or Grok, read the failure message before "fixing" it:
// that change would duplicate a surface those providers already have.
func TestSkillsMenuIsDeliberatelyOllamaOnly(t *testing.T) {
	cfg := policyCfg()

	if !slices.Contains(SystemPromptPolicy(providerOllama, cfg), ElemSkills) {
		t.Error("ollama must receive the skills menu — it has no native skill routing, " +
			"so the menu is how skills reach it at all")
	}

	for _, provider := range []string{providerClaude, providerGrok} {
		if slices.Contains(SystemPromptPolicy(provider, cfg), ElemSkills) {
			t.Errorf("provider %q must NOT receive samantha's skills menu.\n"+
				"This asymmetry is deliberate (not an oversight): %s runs its own tool "+
				"and skill routing, so injecting our progressive-disclosure catalog would "+
				"duplicate that surface, compete with its native tool selection, and spend "+
				"system-prompt budget on instructions it has a better mechanism for.",
				provider, provider)
		}
	}
}

// Error case: environment grounding is opt-out, and opting out must remove it
// everywhere rather than only where it happened to be wired.
func TestEnvironmentContextCanBeDisabledUniformly(t *testing.T) {
	cfg := policyCfg()
	cfg.EnvironmentContextEnabled = false

	for _, provider := range []string{providerOllama, providerClaude, providerGrok} {
		if slices.Contains(SystemPromptPolicy(provider, cfg), ElemEnvironment) {
			t.Errorf("provider %q still includes environment grounding when disabled", provider)
		}
	}
}

// A nil config must behave as the default rather than silently dropping
// grounding — a benchmark or test harness that passes nil should see production
// behaviour.
func TestNilConfigKeepsEnvironmentGrounding(t *testing.T) {
	if !slices.Contains(SystemPromptPolicy(providerOllama, nil), ElemEnvironment) {
		t.Error("nil config must default to environment grounding enabled")
	}
}

// Assembly must follow the policy's order, not just its membership: the persona
// has to lead, or the model reads grounding before it knows who it is.
func TestAssembledPromptFollowsPolicyOrder(t *testing.T) {
	cfg := policyCfg()
	catalog := []skills.Skill{{Name: "hello", Description: "greeting"}}

	got := AssembleSystemPrompt(SystemPromptInput{
		Provider: providerOllama,
		Persona:  "PERSONA_MARKER",
		WorkDir:  "/work",
		Skills:   catalog,
		Turn:     "TURN_MARKER",
		Cfg:      cfg,
	})

	personaAt := strings.Index(got, "PERSONA_MARKER")
	envAt := strings.Index(got, "Environment:")
	skillsAt := strings.Index(got, "Available skills")
	turnAt := strings.Index(got, "TURN_MARKER")

	for _, c := range []struct {
		name string
		idx  int
	}{{"persona", personaAt}, {"environment", envAt}, {"skills", skillsAt}, {"turn", turnAt}} {
		if c.idx < 0 {
			t.Fatalf("assembled prompt is missing the %s element:\n%s", c.name, got)
		}
	}
	if !(personaAt < envAt && envAt < skillsAt && skillsAt < turnAt) {
		t.Errorf("elements out of policy order: persona=%d env=%d skills=%d turn=%d",
			personaAt, envAt, skillsAt, turnAt)
	}
}

// Claude and Grok append the turn instruction to the user message instead, so
// it must not also appear in their system prompt — duplicating it would spend
// budget twice and give the instruction two different positions of emphasis.
func TestTurnInstructionPlacementIsProviderSpecific(t *testing.T) {
	cfg := policyCfg()

	for _, provider := range []string{providerClaude, providerGrok} {
		got := AssembleSystemPrompt(SystemPromptInput{
			Provider: provider,
			Persona:  "PERSONA_MARKER",
			WorkDir:  "/work",
			Turn:     "TURN_MARKER",
			Cfg:      cfg,
		})
		if strings.Contains(got, "TURN_MARKER") {
			t.Errorf("provider %q put the turn instruction in the system prompt; "+
				"it is appended to the user message on this path", provider)
		}
		if TurnGoesInSystemPrompt(provider) {
			t.Errorf("provider %q should not place the turn instruction in the system prompt", provider)
		}
	}

	if !TurnGoesInSystemPrompt(providerOllama) {
		t.Error("ollama rebuilds its system prompt per turn, so the instruction rides there")
	}
}
