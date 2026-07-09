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

	entries := map[string]Entry{}
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
		entries[entryKey(entry.Kind, entry.Name)] = entry
	}

	userEntries, err := catalogUserDocuments(userDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range userEntries {
		entries[entryKey(entry.Kind, entry.Name)] = entry
	}

	list := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
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

func catalogUserDocuments(userDir string) ([]Entry, error) {
	if userDir == "" {
		return nil, nil
	}
	if _, err := os.Stat(userDir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("checking prompts dir: %w", err)
	}

	entries := map[string]Entry{}
	priorities := map[string]int{}
	err := filepath.WalkDir(userDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		priority, ok := promptExtPriority(filepath.Ext(path))
		if !ok {
			return nil
		}

		kind := Kind(filepath.Base(filepath.Dir(path)))
		doc, err := LoadFile(path, kind)
		if err != nil {
			// Fail-safe: a broken user document must not brick listing — the
			// resolver likewise falls back past unloadable files.
			return nil
		}
		if isEmbeddedDefault(doc) {
			return nil
		}

		key := entryKey(doc.Prompt.Kind, doc.Prompt.Name)
		if existing, ok := priorities[key]; ok && existing <= priority {
			return nil
		}
		entries[key] = Entry{
			Kind:   doc.Prompt.Kind,
			Name:   doc.Prompt.Name,
			Source: SourceUser,
			Path:   path,
			Hash:   doc.Hash(),
		}
		priorities[key] = priority
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cataloging user prompts: %w", err)
	}

	list := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry)
	}
	return list, nil
}

func promptExtPriority(ext string) (int, bool) {
	switch ext {
	case ".yaml":
		return 0, true
	case ".yml":
		return 1, true
	case ".md":
		return 2, true
	default:
		return 0, false
	}
}

func entryKey(kind Kind, name string) string {
	return string(kind) + "\x00" + name
}
