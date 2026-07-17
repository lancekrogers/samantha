package skills

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// skillFile is the required filename inside each skill directory.
const skillFile = "SKILL.md"

// Loader discovers SKILL.md folders under one or more roots.
// Prefer Dirs for multi-root harness discovery; Dir is a single-root shortcut.
type Loader struct {
	Dir  string
	Dirs []string
}

// frontmatter is the YAML block at the top of SKILL.md.
type frontmatter struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	AllowedTools any    `yaml:"allowed-tools"`
}

// Catalog walks each search root for */SKILL.md, parses each skill, and
// returns the merged catalog. Missing or empty roots yield no skills (not an
// error). Malformed skills are skipped. When the same name appears in more
// than one root, the first root in Dirs/Dir order wins (project over system).
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

// catalogDir walks a single skills root into byName. First-wins: existing
// names are not overwritten.
func catalogDir(ctx context.Context, dir string, byName map[string]Skill) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking skills dir %s: %w", dir, err)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(d.Name(), skillFile) {
			return nil
		}

		skill, err := loadSkill(path)
		if err != nil {
			// Fail-safe: skip malformed skills rather than failing the catalog.
			return nil
		}
		if _, exists := byName[skill.Name]; exists {
			return nil
		}
		byName[skill.Name] = skill
		return nil
	})
	if err != nil {
		return fmt.Errorf("cataloging skills in %s: %w", dir, err)
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

	return Skill{
		Name:         name,
		Description:  desc,
		Body:         body,
		Dir:          filepath.Dir(path),
		AllowedTools: parseAllowedTools(meta.AllowedTools),
	}, nil
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

func parseAllowedTools(v any) []string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		parts := strings.FieldsFunc(t, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
