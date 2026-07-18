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
	// ReadDir order is not guaranteed across platforms for first-win within
	// one root; either description is acceptable as long as only one entry.
	if got[0].Name != "dup" {
		t.Errorf("name = %q, want dup", got[0].Name)
	}
}

func TestCatalogOnlyImmediateChildren(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Valid one-level skill.
	writeSkill(t, filepath.Join(root, "top"), "top", "top-level", "body top")
	// Nested under an intermediate dir — must NOT be discovered.
	nested := filepath.Join(root, "nested", "deep")
	writeSkill(t, nested, "deep", "should not load", "body deep")
	// SKILL.md sitting directly in the root (not in a child dir) is ignored.
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: rootfile\ndescription: no\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Loader{Dir: root}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "top" {
		t.Fatalf("got %v, want only top-level skill", names(got))
	}
}

func TestCatalogTruncatesLongDescription(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// description longer than MaxDescriptionRunes
	long := strings.Repeat("x", MaxDescriptionRunes+50)
	writeSkill(t, filepath.Join(root, "long"), "long", long, "body")

	got, err := Loader{Dir: root}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1", len(got))
	}
	if n := len([]rune(got[0].Description)); n != MaxDescriptionRunes {
		t.Fatalf("description runes = %d, want %d", n, MaxDescriptionRunes)
	}
	if !strings.HasSuffix(got[0].Description, "…") {
		t.Fatalf("truncated description should end with ellipsis: %q", got[0].Description)
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	if TruncateRunes("hi", 10) != "hi" {
		t.Fatal("short string should be unchanged")
	}
	if got := TruncateRunes("abcdef", 4); got != "abc…" {
		t.Fatalf("got %q, want abc…", got)
	}
	if TruncateRunes("x", 0) != "x" {
		t.Fatal("max<=0 should leave string unchanged")
	}
}

func TestCatalogMultiDirPrecedence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project")
	system := filepath.Join(root, "system")
	writeSkill(t, filepath.Join(project, "shared"), "shared", "project wins", "project")
	writeSkill(t, filepath.Join(system, "shared"), "shared", "system loses", "system")
	writeSkill(t, filepath.Join(system, "only-system"), "only-system", "system only", "sys")

	got, err := Loader{Dirs: []string{project, system}}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills %v, want 2", len(got), names(got))
	}
	if byName["shared"].Description != "project wins" {
		t.Errorf("shared description = %q, want project wins", byName["shared"].Description)
	}
	if _, ok := byName["only-system"]; !ok {
		t.Error("missing only-system skill from second root")
	}
}

func TestDefaultSearchPaths(t *testing.T) {
	// Not parallel: overrides package userHomeDir.
	home := t.TempDir()
	restore := SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)

	work := "/tmp/samantha-launch-dir"
	configured := "/custom/samantha/skills"
	got := DefaultSearchPaths(work, configured)
	want := []string{
		filepath.Join(work, ".claude", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Clean(configured),
	}
	if len(got) != len(want) {
		t.Fatalf("DefaultSearchPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Empty workDir still includes user + configured.
	got = DefaultSearchPaths("", configured)
	if len(got) != 2 || got[0] != filepath.Join(home, ".claude", "skills") || got[1] != filepath.Clean(configured) {
		t.Fatalf("empty workDir paths = %v", got)
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
