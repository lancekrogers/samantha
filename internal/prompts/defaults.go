package prompts

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed defaults/*.yaml
var defaultsFS embed.FS

// Default returns the embedded default document for a kind, one per kind
// at defaults/<kind>.yaml.
func Default(kind Kind) (*Document, error) {
	data, err := defaultsFS.ReadFile("defaults/" + string(kind) + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("no embedded default prompt for kind %q", kind)
	}
	doc, err := Load(data)
	if err != nil {
		return nil, fmt.Errorf("embedded default %q: %w", kind, err)
	}
	return doc, nil
}

// Resolver locates prompt documents with fixed precedence: an explicit
// path, then the user prompts directory (layout <dir>/<kind>/<name>.yaml
// or .md), then the embedded defaults.
type Resolver struct {
	Path    string // explicit document path; wins when set
	UserDir string // user prompts directory; empty skips the layer
}

// Resolve loads the document for kind and name at the highest-precedence
// location that has one.
func (r Resolver) Resolve(kind Kind, name string) (*Document, error) {
	if r.Path != "" {
		return LoadFile(r.Path, kind)
	}
	if r.UserDir != "" {
		for _, ext := range []string{".yaml", ".yml", ".md"} {
			path := filepath.Join(r.UserDir, string(kind), name+ext)
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("checking prompt file: %w", err)
			}
			return LoadFile(path, kind)
		}
	}
	return Default(kind)
}
