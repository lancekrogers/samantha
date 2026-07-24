package prompts

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// or .md), then the embedded default for that kind — only when the requested
// name is empty or matches the embedded document's name.
//
// A missing user document named "uncle-fu" must NOT silently return the
// embedded "samantha" persona. That footgun made the Personas editor and
// brain load the wrong identity whenever a profile's prompt ref was stale.
type Resolver struct {
	Path    string // explicit document path; wins when set
	UserDir string // user prompts directory; empty skips the layer
}

// Resolve loads the document for kind and name at the highest-precedence
// location that has one.
func (r Resolver) Resolve(kind Kind, name string) (*Document, error) {
	name = strings.TrimSpace(name)
	if r.Path != "" {
		return LoadFile(r.Path, kind)
	}
	if r.UserDir != "" && name != "" {
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
	return resolveEmbedded(kind, name)
}

// resolveEmbedded returns the one-per-kind embedded default only when the
// caller asked for it (empty name) or for the document's real name.
func resolveEmbedded(kind Kind, name string) (*Document, error) {
	doc, err := Default(kind)
	if err != nil {
		if name == "" {
			return nil, err
		}
		return nil, fmt.Errorf("no %s prompt named %q", kind, name)
	}
	if name == "" || name == doc.Prompt.Name {
		return doc, nil
	}
	return nil, fmt.Errorf("no %s prompt named %q", kind, name)
}
