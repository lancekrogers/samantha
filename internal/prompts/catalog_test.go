package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogEmbeddedDefaults(t *testing.T) {
	entries, err := Catalog("")
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}

	byKind := map[Kind]Entry{}
	for _, e := range entries {
		byKind[e.Kind] = e
	}
	for _, kind := range []Kind{KindPersona, KindTurn} {
		e, ok := byKind[kind]
		if !ok {
			t.Fatalf("Catalog() missing kind %s", kind)
		}
		if e.Source != SourceEmbedded {
			t.Errorf("kind %s source = %s, want embedded", kind, e.Source)
		}
		if e.Hash == "" {
			t.Errorf("kind %s hash empty", kind)
		}
	}
}

func TestCatalogReportsUserOverride(t *testing.T) {
	dir := t.TempDir()
	personaDir := filepath.Join(dir, "persona")
	if err := os.MkdirAll(personaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "schema: samantha.prompt.v1\nprompt:\n  name: samantha\n  kind: persona\n  system_prompt: Custom persona.\n"
	if err := os.WriteFile(filepath.Join(personaDir, "samantha.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Catalog(dir)
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	var persona Entry
	for _, e := range entries {
		if e.Kind == KindPersona {
			persona = e
		}
	}
	if persona.Source != SourceUser {
		t.Errorf("persona source = %s, want user", persona.Source)
	}
	if persona.Path == "" {
		t.Error("persona path empty for a user override")
	}
}

func TestCatalogIncludesUserOnlyDocuments(t *testing.T) {
	dir := t.TempDir()
	styleDir := filepath.Join(dir, "style")
	if err := os.MkdirAll(styleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(styleDir, "casual.md")
	if err := os.WriteFile(path, []byte("Speak casually.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Catalog(dir)
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	var style Entry
	for _, e := range entries {
		if e.Kind == KindStyle && e.Name == "casual" {
			style = e
		}
	}
	if style.Source != SourceUser {
		t.Errorf("style source = %s, want user", style.Source)
	}
	if style.Path != path {
		t.Errorf("style path = %q, want %q", style.Path, path)
	}
	if style.Hash == "" {
		t.Error("style hash empty")
	}
}

func TestCatalogReportsSeededDefaultsAsEmbedded(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	entries, err := Catalog(dir)
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}

	for _, e := range entries {
		if e.Source != SourceEmbedded {
			t.Errorf("kind %s source = %s, want embedded for seeded default", e.Kind, e.Source)
		}
		if e.Path != "" {
			t.Errorf("kind %s path = %q, want empty for seeded default", e.Kind, e.Path)
		}
	}
}
