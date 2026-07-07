package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Assemble renders the system prompt deterministically. Section order is
// pinned: identity, conversation style, guidance, constraints, core
// concepts (key-sorted). Absent sections contribute nothing. Placeholders
// are left unresolved.
func (d *Document) Assemble() string {
	sp := d.Prompt.SystemPrompt
	sections := []string{strings.TrimRight(sp.Identity, "\n")}
	if len(sp.ConversationStyle) > 0 {
		sections = append(sections, renderList("Conversation style:", sp.ConversationStyle))
	}
	if len(sp.Guidance) > 0 {
		sections = append(sections, renderList("Guidance:", sp.Guidance))
	}
	if len(sp.Constraints) > 0 {
		sections = append(sections, renderList("Constraints:", sp.Constraints))
	}
	if len(sp.CoreConcepts) > 0 {
		keys := make([]string, 0, len(sp.CoreConcepts))
		for k := range sp.CoreConcepts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("Core concepts:")
		for _, k := range keys {
			b.WriteString("\n")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(sp.CoreConcepts[k])
		}
		sections = append(sections, b.String())
	}
	return strings.Join(sections, "\n\n")
}

// Hash returns the sha256 hex digest of the assembled text with
// placeholders unresolved. Resume keys depend on it staying stable.
func (d *Document) Hash() string {
	sum := sha256.Sum256([]byte(d.Assemble()))
	return hex.EncodeToString(sum[:])
}

func renderList(label string, items []string) string {
	var b strings.Builder
	b.WriteString(label)
	for _, item := range items {
		b.WriteString("\n- ")
		b.WriteString(item)
	}
	return b.String()
}
