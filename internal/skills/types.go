// Package skills loads Agent Skills (Anthropic SKILL.md) from a directory.
// Skills are discovered via progressive disclosure: catalog name+description
// first, full body on demand.
package skills

// Skill is a discovered Agent Skill folder (SKILL.md + optional bundled files).
//
// allowed-tools from frontmatter is not loaded: without an "active skill"
// scope in the tool loop, enforcing a per-skill allow-list would be a false
// security contract. Revisit when the agent loop tracks skill activation.
type Skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
}
