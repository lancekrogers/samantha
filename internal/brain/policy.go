package brain

import (
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/skills"
)

// PromptElement names one component of what a provider's model actually sees.
type PromptElement string

const (
	// ElemPersona is the persona system prompt — who the agent is.
	ElemPersona PromptElement = "persona"
	// ElemEnvironment is machine grounding: user, working directory, host, OS.
	ElemEnvironment PromptElement = "environment"
	// ElemSkills is the progressive-disclosure skills menu.
	ElemSkills PromptElement = "skills"
	// ElemTurn is the per-reply voice instruction ("2-3 sentences, no markdown").
	ElemTurn PromptElement = "turn"
)

// Provider names used by the prompt policy.
const (
	providerOllama = "ollama"
	providerClaude = "claude"
	providerGrok   = "grok"
)

// SystemPromptPolicy is the single source of truth for which elements a
// provider's prompt contains, and in what order.
//
// It exists because the providers had silently diverged: ollama received
// environment grounding but no turn instruction, while Claude and Grok received
// the turn instruction but no environment grounding. The same persona therefore
// behaved differently depending on which brain was selected, with nothing in the
// UI to explain why.
//
// The rule this encodes:
//
//	Elements that define the agent's personality and grounding are identical
//	across providers. Elements that adapt to a provider's native capabilities
//	are not, and each such exception must state its reason.
//
// There is exactly one exception today — see skillMenuApplies.
func SystemPromptPolicy(provider string, cfg *config.Config) []PromptElement {
	elems := []PromptElement{ElemPersona}
	if environmentContextEnabled(cfg) {
		elems = append(elems, ElemEnvironment)
	}
	if skillMenuApplies(provider) {
		elems = append(elems, ElemSkills)
	}
	elems = append(elems, ElemTurn)
	return elems
}

// skillMenuApplies reports whether a provider receives samantha's skills menu.
//
// **Deliberate asymmetry, not an oversight.** Claude and Grok run their own tool
// and skill routing; injecting samantha's progressive-disclosure catalog into
// their system prompt would duplicate a surface they already have, compete with
// their native tool selection, and spend system-prompt budget on instructions
// the model has a better mechanism for. Ollama has no such surface, so the menu
// is how skills reach it at all.
//
// If ollama's routing is ever replaced by native tool-calling, revisit this.
func skillMenuApplies(provider string) bool {
	return normalizeProvider(provider) == providerOllama
}

// environmentContextEnabled reports whether machine grounding is included.
// Default is on; the config key exists for users who find the block noisy.
func environmentContextEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.EnvironmentContextEnabled
}

// SystemPromptInput carries the resolved text for each element.
type SystemPromptInput struct {
	Provider string
	Persona  string
	WorkDir  string
	Skills   []skills.Skill
	// Turn is the per-reply instruction. Whether it is appended to the system
	// prompt or to the user message is provider mechanics (see TurnGoesInSystemPrompt);
	// its *presence* is policy.
	Turn string
	Cfg  *config.Config
}

// AssembleSystemPrompt builds a provider's system prompt from the shared policy,
// so every provider gets the same elements in the same order.
func AssembleSystemPrompt(in SystemPromptInput) string {
	var b strings.Builder
	for _, elem := range SystemPromptPolicy(in.Provider, in.Cfg) {
		switch elem {
		case ElemPersona:
			b.WriteString(in.Persona)
		case ElemEnvironment:
			b.WriteString("\n")
			b.WriteString(EnvironmentContext(in.WorkDir))
		case ElemSkills:
			if sc := SkillContext(in.Skills); sc != "" {
				b.WriteString(sc)
			}
		case ElemTurn:
			// Claude and Grok append the turn instruction to the user message
			// instead, so that the meta-turn override (OmitTurnInstruction) can
			// drop it per turn without rebuilding the system prompt.
			if TurnGoesInSystemPrompt(in.Provider) && strings.TrimSpace(in.Turn) != "" {
				b.WriteString("\n\n")
				b.WriteString(in.Turn)
			}
		}
	}
	return b.String()
}

// TurnGoesInSystemPrompt reports where the turn instruction is placed.
//
// Ollama rebuilds its system prompt per turn anyway, so the instruction rides
// there. Claude and Grok send a persistent system prompt and a fresh user
// message each turn, so appending it to the message is what lets a meta turn
// (/compact's summarize) drop it without disturbing the cached system prompt.
// Either way the model sees it — that is what the policy guarantees.
func TurnGoesInSystemPrompt(provider string) bool {
	return normalizeProvider(provider) == providerOllama
}
