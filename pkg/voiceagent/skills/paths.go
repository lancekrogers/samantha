package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir resolves the user home directory. Tests may override it via
// SetUserHomeDirForTest to avoid picking up real ~/.agents/skills.
var userHomeDir = os.UserHomeDir

// SetUserHomeDirForTest overrides home resolution for tests. The returned
// function restores the previous resolver; call it from t.Cleanup.
func SetUserHomeDirForTest(fn func() (string, error)) (restore func()) {
	prev := userHomeDir
	if fn == nil {
		userHomeDir = os.UserHomeDir
	} else {
		userHomeDir = fn
	}
	return func() { userHomeDir = prev }
}

// DefaultSearchPaths returns skill directories in precedence order (first match
// wins for duplicate skill names).
//
// Follows the cross-client Agent Skills convention
// (https://agentskills.io/client-implementation/adding-skills-support):
//
//  1. <workDir>/.agents/skills — project skills (Codex, VS Code, camp, …)
//  2. nearest ancestor .agents/skills — workspace/project-root skills
//  3. ~/.agents/skills         — user skills shared across tools
//  4. configuredDir            — Samantha skills_dir (or its default under config)
//
// Ollama does not scan .claude/skills: that is Claude Code's native path and
// is handled by the Claude provider harness. Scanning both would duplicate
// skills when camp projects the same set into both locations.
//
// Empty or duplicate paths are omitted. Missing directories are fine — Catalog
// treats them as empty.
func DefaultSearchPaths(workDir, configuredDir string) []string {
	var paths []string
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	if strings.TrimSpace(workDir) != "" {
		add(filepath.Join(workDir, ".agents", "skills"))
		if ancestor := ancestorAgentSkillsDir(workDir); ancestor != "" {
			add(ancestor)
		}
	}
	if home, err := userHomeDir(); err == nil {
		add(filepath.Join(home, ".agents", "skills"))
	}
	add(configuredDir)
	return paths
}

// ancestorAgentSkillsDir returns the nearest existing .agents/skills directory
// above workDir. The current directory is handled separately by
// DefaultSearchPaths so project-local skills keep precedence.
func ancestorAgentSkillsDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}

	resolved, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}

	for {
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return ""
		}
		candidate := filepath.Join(parent, ".agents", "skills")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		resolved = parent
	}
}
