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

// startersFS holds minimal per-kind templates that seed new user-authored
// prompts (e.g. the create-persona form). Kept out of defaults/ so Seed and
// Catalog never treat them as runtime prompt documents.
//
//go:embed starters/*.yaml
var startersFS embed.FS

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

// Starter returns the embedded starter document for a kind — a minimal
// template meant to seed new user-authored prompts — at starters/<kind>.yaml.
func Starter(kind Kind) (*Document, error) {
	data, err := startersFS.ReadFile("starters/" + string(kind) + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("no embedded starter prompt for kind %q", kind)
	}
	doc, err := Load(data)
	if err != nil {
		return nil, fmt.Errorf("embedded starter %q: %w", kind, err)
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
