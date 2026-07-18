package skills

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// skillFile is the required filename inside each skill directory.
const skillFile = "SKILL.md"

// MaxDescriptionRunes caps skill descriptions stored in the catalog and
// advertised in the system prompt (progressive disclosure should stay short).
const MaxDescriptionRunes = 400

// Loader discovers SKILL.md folders under one or more roots.
// Prefer Dirs for multi-root harness discovery; Dir is a single-root shortcut.
type Loader struct {
	Dir  string
	Dirs []string
}

// frontmatter is the YAML block at the top of SKILL.md.
// allowed-tools is intentionally not parsed: the agent loop has no "active
// skill" scope yet, so advertising an allow-list would be a false contract.
// Unknown YAML keys (including allowed-tools) are ignored by the decoder.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Catalog scans each search root for immediate child skill folders
// (<root>/<name>/SKILL.md), parses each skill, and returns the merged catalog.
// Missing or empty roots yield no skills (not an error). Malformed skills are
// skipped. When the same name appears in more than one root, the first root in
// Dirs/Dir order wins (project over system). Nested trees are not walked.
func (l Loader) Catalog(ctx context.Context) ([]Skill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	byName := map[string]Skill{}
	for _, dir := range l.searchDirs() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := catalogDir(ctx, dir, byName); err != nil {
			return nil, err
		}
	}

	list := make([]Skill, 0, len(byName))
	for _, s := range byName {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list, nil
}

func (l Loader) searchDirs() []string {
	if len(l.Dirs) > 0 {
		out := make([]string, 0, len(l.Dirs))
		for _, d := range l.Dirs {
			if d != "" {
				out = append(out, d)
			}
		}
		return out
	}
	if l.Dir != "" {
		return []string{l.Dir}
	}
	return nil
}

// catalogDir loads skills from immediate children of dir only:
// <dir>/<skill-name>/SKILL.md. First-wins: existing names are not overwritten.
func catalogDir(ctx context.Context, dir string, byName map[string]Skill) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking skills dir %s: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing skills in %s: %w", dir, err)
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !e.IsDir() {
			continue
		}
		// Skip hidden dirs (e.g. .git) under a skills root.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name(), skillFile)
		skill, err := loadSkill(path)
		if err != nil {
			// Fail-safe: skip missing/malformed skills rather than failing the catalog.
			continue
		}
		if _, exists := byName[skill.Name]; exists {
			continue
		}
		byName[skill.Name] = skill
	}
	return nil
}

func loadSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("reading skill file: %w", err)
	}

	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %w", path, err)
	}

	var meta frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	if err := dec.Decode(&meta); err != nil {
		return Skill{}, fmt.Errorf("%s: parsing frontmatter: %w", path, err)
	}

	name := strings.TrimSpace(meta.Name)
	if name == "" {
		return Skill{}, fmt.Errorf("%s: missing frontmatter name", path)
	}
	desc := strings.TrimSpace(meta.Description)
	if desc == "" {
		return Skill{}, fmt.Errorf("%s: missing frontmatter description", path)
	}
	desc = TruncateRunes(desc, MaxDescriptionRunes)

	return Skill{
		Name:        name,
		Description: desc,
		Body:        body,
		Dir:         filepath.Dir(path),
	}, nil
}

// TruncateRunes shortens s to at most max runes, appending "…" when truncated.
// max <= 0 leaves s unchanged. A single-rune budget becomes "…".
func TruncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	// Reserve one rune for the ellipsis.
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// splitFrontmatter extracts the YAML between leading --- fences and the
// markdown body after the closing fence. Files without a valid frontmatter
// fence are rejected.
func splitFrontmatter(data []byte) (yamlBlock []byte, body string, err error) {
	text := string(data)
	// Normalize newlines for scanning.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimLeft(text, " \t\n")
	if !strings.HasPrefix(text, "---") {
		return nil, "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := text[len("---"):]
	// Optional newline after opening fence.
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("unclosed YAML frontmatter")
	}
	yamlBlock = []byte(rest[:end])
	after := rest[end+len("\n---"):]
	after = strings.TrimPrefix(after, "\n")
	return yamlBlock, after, nil
}
