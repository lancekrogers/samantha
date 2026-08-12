package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	created, err := Seed(dir)
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	want := map[string]bool{
		filepath.Join("persona", "samantha.yaml"): true,
		filepath.Join("turn", "samantha.yaml"):    true,
	}
	for _, rel := range created {
		delete(want, rel)
	}
	if len(want) != 0 {
		t.Errorf("Seed() missing expected files %v (created %v)", want, created)
	}

	// Seeded files must load and match the embedded defaults' hashes.
	for _, kind := range []Kind{KindPersona, KindTurn} {
		path := filepath.Join(dir, string(kind), "samantha.yaml")
		doc, err := LoadFile(path, kind)
		if err != nil {
			t.Fatalf("loading seeded %s: %v", path, err)
		}
		def, err := Default(kind)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Hash() != def.Hash() {
			t.Errorf("seeded %s hash %s != embedded %s", kind, doc.Hash(), def.Hash())
		}
	}
}

func TestSeedNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "persona", "samantha.yaml")
	custom := []byte("schema: samantha.prompt.v1\nprompt:\n  name: samantha\n  kind: persona\n  system_prompt: Custom.\n")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := Seed(dir)
	if err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second Seed() created %v, want nothing (idempotent)", created)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Error("Seed() overwrote a user-edited file")
	}
}
