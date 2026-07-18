// Package skills loads Agent Skills (Anthropic SKILL.md) from a directory.
// Skills are discovered via progressive disclosure: catalog name+description
// first, full body on demand.
package skills

// Skill is a discovered Agent Skill folder (SKILL.md + optional bundled files).
type Skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
	// AllowedTools is optional frontmatter allowed-tools (Agent Skills
	// experimental). Parsed and surfaced as a soft hint after read_skill;
	// it does not remove CLI tools. Values map to Samantha tool names
	// (list_files, read_file, write_file, run_command) with common aliases
	// (Read → read_file, Bash → run_command). See agentskills.io specification.
	AllowedTools []string
}
