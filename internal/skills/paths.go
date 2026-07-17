package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir resolves the user home directory. Tests may override it via
// SetUserHomeDirForTest to avoid picking up real ~/.claude/skills.
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
// wins for duplicate skill names). Layout matches common agent harnesses:
//
//  1. <workDir>/.claude/skills — project skills for the launch/cwd directory
//  2. ~/.claude/skills         — user/system skills shared across tools
//  3. configuredDir            — Samantha skills_dir (or its default under config)
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
		add(filepath.Join(workDir, ".claude", "skills"))
	}
	if home, err := userHomeDir(); err == nil {
		add(filepath.Join(home, ".claude", "skills"))
	}
	add(configuredDir)
	return paths
}
