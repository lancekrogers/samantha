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
