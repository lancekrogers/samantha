package persona

import (
	"fmt"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
)

// PromptHashPrefix is how many hex characters of prompts.Document.Hash() the
// CLI and the wire carry. Twelve is enough for a client to tell "the prompt I
// edited is the prompt the model now sees" without shipping a 64-char digest
// through every ack.
const PromptHashPrefix = 12

// PromptDoc describes the kind=persona prompt document a profile resolves to.
// It is the persona package's answer to "which identity text is this persona
// actually running", so no CLI or wire surface has to re-derive prompt
// resolution — that logic lives here beside effectivePersonaRef.
type PromptDoc struct {
	// Name is the catalog name the resolver used.
	Name string
	// Path is the user document's file path; empty when Source is embedded.
	Path string
	// Source is "user" or "embedded". An embedded document is shared, so an
	// editor must copy before it writes.
	Source string
	// Structured reports the strict mapping form (identity plus any of
	// conversation_style / guidance / constraints / core_concepts). Writing a
	// flat body over one of these destroys the extra sections.
	Structured bool
	// Hash is the first PromptHashPrefix hex chars of the assembled document's
	// sha256, with placeholders unresolved.
	Hash string
	// Body is the raw identity text an editor loads. Empty when Structured or
	// when Source is embedded, because neither is safe to flat-edit in place.
	Body string
}

// DescribePrompt reports the persona prompt document backing p, resolved the
// same way the running brain resolves it (effectivePersonaRef, then the
// user-dir-first resolver).
func DescribePrompt(p *Profile) (PromptDoc, error) {
	if p == nil {
		return PromptDoc{}, fmt.Errorf("persona profile: nil")
	}
	ref := effectivePersonaRef(p)
	if ref == "" {
		ref = DefaultID
	}
	entry, err := prompts.Describe(promptsDir(), prompts.KindPersona, ref)
	if err != nil {
		return PromptDoc{}, fmt.Errorf("describing persona prompt %q: %w", ref, err)
	}
	doc, err := prompts.Resolver{UserDir: promptsDir()}.Resolve(prompts.KindPersona, ref)
	if err != nil {
		return PromptDoc{}, fmt.Errorf("resolving persona prompt %q: %w", ref, err)
	}
	out := PromptDoc{
		Name:       ref,
		Path:       entry.Path,
		Source:     string(entry.Source),
		Structured: IsStructuredPrompt(doc),
		Hash:       shortHash(doc.Hash()),
	}
	if !out.Structured && entry.Source == prompts.SourceUser {
		out.Body = doc.Prompt.SystemPrompt.Identity
	}
	return out, nil
}

// PromptHashFor returns the short assembled-document hash for the persona
// prompt named ref. serve uses it to fill set_persona_ack.prompt_hash, so the
// ack answers "which prompt text" rather than repeating the document name.
func PromptHashFor(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = DefaultID
	}
	doc, err := prompts.Resolver{UserDir: promptsDir()}.Resolve(prompts.KindPersona, ref)
	if err != nil {
		return "", fmt.Errorf("resolving persona prompt %q: %w", ref, err)
	}
	return shortHash(doc.Hash()), nil
}

// IsStructuredPrompt reports whether doc uses the strict mapping form. A
// document carrying only identity assembles to exactly that text, so replacing
// it with a flat body loses nothing and is not structured.
func IsStructuredPrompt(doc *prompts.Document) bool {
	if doc == nil {
		return false
	}
	sp := doc.Prompt.SystemPrompt
	return len(sp.ConversationStyle) > 0 || len(sp.Guidance) > 0 ||
		len(sp.Constraints) > 0 || len(sp.CoreConcepts) > 0
}

func shortHash(sum string) string {
	if len(sum) <= PromptHashPrefix {
		return sum
	}
	return sum[:PromptHashPrefix]
}
