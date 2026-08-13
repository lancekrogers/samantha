package prompts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Seed writes the embedded default documents into dir, one per kind at
// dir/<kind>/<name>.yaml, so a user can discover and edit a real starting
// file instead of authoring the schema from scratch. It never overwrites an
// existing file and is safe to call on every startup; it returns the relative
// paths it created (empty on a fully-seeded directory).
func Seed(dir string) ([]string, error) {
	entries, err := fs.ReadDir(defaultsFS, "defaults")
	if err != nil {
		return nil, fmt.Errorf("reading embedded defaults: %w", err)
	}

	var created []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := defaultsFS.ReadFile("defaults/" + entry.Name())
		if err != nil {
			return created, fmt.Errorf("reading embedded default %s: %w", entry.Name(), err)
		}
		// Load to derive the on-disk path (kind/name) from the document itself;
		// the raw bytes are what we write, preserving comments and the schema
		// header.
		doc, err := Load(data)
		if err != nil {
			return created, fmt.Errorf("embedded default %s: %w", entry.Name(), err)
		}

		rel := filepath.Join(string(doc.Prompt.Kind), doc.Prompt.Name+".yaml")
		dst := filepath.Join(dir, rel)
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return created, fmt.Errorf("checking %s: %w", dst, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return created, fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return created, fmt.Errorf("writing %s: %w", dst, err)
		}
		created = append(created, rel)
	}

	return created, nil
}
