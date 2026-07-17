// Package skills loads Agent Skills (Anthropic SKILL.md) from a directory.
// Skills are discovered via progressive disclosure: catalog name+description
// first, full body on demand.
package skills

// Skill is a discovered Agent Skill folder (SKILL.md + optional bundled files).
type Skill struct {
	Name         string
	Description  string
	Body         string
	Dir          string
	AllowedTools []string
}
