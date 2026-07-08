package prompts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Source identifies where a resolved document came from.
type Source string

const (
	SourceEmbedded Source = "embedded"
	SourceUser     Source = "user"
)

// Entry describes one prompt document as the resolver sees it.
type Entry struct {
	Kind   Kind   `json:"kind"`
	Name   string `json:"name"`
	Source Source `json:"source"`
	Path   string `json:"path,omitempty"`
	Hash   string `json:"hash"`
}

// Catalog reports the active prompt documents: one per embedded default kind,
// each marked user when a document in userDir shadows it, otherwise embedded.
// Entries are sorted by kind then name.
func Catalog(userDir string) ([]Entry, error) {
	embedded, err := fs.ReadDir(defaultsFS, "defaults")
	if err != nil {
		return nil, fmt.Errorf("reading embedded defaults: %w", err)
	}

	var entries []Entry
	for _, e := range embedded {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := defaultsFS.ReadFile("defaults/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading embedded default %s: %w", e.Name(), err)
		}
		doc, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("embedded default %s: %w", e.Name(), err)
		}
		entry, err := Describe(userDir, doc.Prompt.Kind, doc.Prompt.Name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Describe reports how the resolver would satisfy kind/name: a user document in
// userDir when present, otherwise the embedded default.
func Describe(userDir string, kind Kind, name string) (Entry, error) {
	if path, ok := userDocPath(userDir, kind, name); ok {
		doc, err := LoadFile(path, kind)
		if err != nil {
			return Entry{}, err
		}
		if isEmbeddedDefault(doc) {
			return Entry{Kind: kind, Name: name, Source: SourceEmbedded, Hash: doc.Hash()}, nil
		}
		return Entry{Kind: kind, Name: name, Source: SourceUser, Path: path, Hash: doc.Hash()}, nil
	}
	doc, err := Default(kind)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Kind: kind, Name: name, Source: SourceEmbedded, Hash: doc.Hash()}, nil
}

func isEmbeddedDefault(doc *Document) bool {
	embedded, err := Default(doc.Prompt.Kind)
	if err != nil {
		return false
	}
	return doc.Prompt.Name == embedded.Prompt.Name && doc.Hash() == embedded.Hash()
}

// userDocPath returns the first existing user document for kind/name, honoring
// the resolver's extension precedence (.yaml, .yml, .md).
func userDocPath(userDir string, kind Kind, name string) (string, bool) {
	if userDir == "" {
		return "", false
	}
	for _, ext := range []string{".yaml", ".yml", ".md"} {
		path := filepath.Join(userDir, string(kind), name+ext)
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}
