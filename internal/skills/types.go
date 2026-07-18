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
	// AllowedTools is the optional frontmatter allowed-tools list: tools that
	// may run while this skill is active (after read_skill). Empty means no
	// restriction. Values are matched against Samantha tool names
	// (list_files, read_file, write_file, run_command) with common aliases
	// (Read → read_file, Bash → run_command). See agentskills.io specification.
	AllowedTools []string
}
