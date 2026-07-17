package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogEmptyAndMissingDir(t *testing.T) {
	t.Parallel()

	got, err := Loader{Dir: ""}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("empty Dir: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty Dir: got %d skills, want 0", len(got))
	}

	missing := filepath.Join(t.TempDir(), "no-such-skills")
	got, err = Loader{Dir: missing}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("missing Dir: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing Dir: got %d skills, want 0", len(got))
	}
}

func TestCatalogFixtureDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	loader := Loader{Dir: filepath.Join("testdata", "skills")}
	got, err := loader.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}

	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}

	// Valid skill is present with name/description/body/dir populated.
	valid, ok := byName["hello"]
	if !ok {
		t.Fatalf("Catalog() missing skill %q; got names %v", "hello", names(got))
	}
	if valid.Description != "A friendly greeting skill for tests." {
		t.Errorf("hello.Description = %q, want greeting description", valid.Description)
	}
	if valid.Body == "" {
		t.Error("hello.Body empty")
	}
	if !strings.Contains(valid.Body, "Say hello") {
		t.Errorf("hello.Body missing expected content: %q", valid.Body)
	}
	if valid.Dir == "" {
		t.Error("hello.Dir empty")
	}
	if len(valid.AllowedTools) != 2 || valid.AllowedTools[0] != "run_command" {
		t.Errorf("hello.AllowedTools = %v, want [run_command read_file]", valid.AllowedTools)
	}

	// Missing/malformed frontmatter must be skipped, not hard-fail.
	if _, ok := byName[""]; ok {
		t.Error("empty-name skill should not appear in catalog")
	}
	if _, ok := byName["broken"]; ok {
		t.Error("broken skill (no frontmatter) should be skipped")
	}

	// Non-skill markdown under a skill dir must not invent skills.
	if _, ok := byName["notes"]; ok {
		t.Error("notes.md must not be loaded as a skill")
	}

	// Only the valid fixture skill should be returned.
	if len(got) != 1 {
		t.Fatalf("Catalog() returned %d skills %v, want 1 (hello only)", len(got), names(got))
	}
}

func TestCatalogContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Loader{Dir: filepath.Join("testdata", "skills")}.Catalog(ctx)
	if err == nil {
		t.Fatal("Catalog() with canceled context: want error, got nil")
	}
}

func TestCatalogSkipsDuplicateNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "a"), "dup", "first", "body a")
	writeSkill(t, filepath.Join(dir, "b"), "dup", "second", "body b")

	got, err := Loader{Dir: dir}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1 (first duplicate wins)", len(got))
	}
	// WalkDir order is not guaranteed across platforms for first-win; either
	// description is acceptable as long as only one entry is kept.
	if got[0].Name != "dup" {
		t.Errorf("name = %q, want dup", got[0].Name)
	}
}

func writeSkill(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(skills []Skill) []string {
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.Name
	}
	return out
}
