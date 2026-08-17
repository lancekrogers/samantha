package skills

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
	// Fixture declares allowed-tools: Read list_files
	if len(valid.AllowedTools) != 2 {
		t.Fatalf("hello.AllowedTools = %v, want 2 tokens", valid.AllowedTools)
	}
	if valid.AllowedTools[0] != "Read" || valid.AllowedTools[1] != "list_files" {
		t.Errorf("hello.AllowedTools = %v, want [Read list_files]", valid.AllowedTools)
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

func TestCatalogFollowsSkillDirectorySymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "campaign-skill")
	writeSkill(t, target, "campaign-skill", "projected campaign skill", "follow the link")
	if err := os.Symlink(target, filepath.Join(root, "campaign-skill")); err != nil {
		t.Skipf("creating directory symlink: %v", err)
	}
	// A broken directory projection must remain fail-safe and be ignored.
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Skipf("creating broken symlink: %v", err)
	}

	got, err := Loader{Dir: root}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "campaign-skill" {
		t.Fatalf("got %v, want projected campaign-skill only", names(got))
	}
	if got[0].Dir != filepath.Join(root, "campaign-skill") {
		t.Fatalf("skill Dir = %q, want projected path %q", got[0].Dir, filepath.Join(root, "campaign-skill"))
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

func TestToolAllowed(t *testing.T) {
	t.Parallel()
	if !ToolAllowed("run_command", nil) {
		t.Fatal("empty allow-list should allow everything")
	}
	if !ToolAllowed("read_file", []string{"Read", "list_files"}) {
		t.Fatal("Read alias should match read_file")
	}
	if !ToolAllowed("run_command", []string{"Bash(git:*)"}) {
		t.Fatal("Bash(...) should match run_command")
	}
	if !ToolAllowed("web_search", []string{"WebSearch"}) {
		t.Fatal("WebSearch alias should match web_search")
	}
	if !ToolAllowed("fetch_url", []string{"WebFetch"}) {
		t.Fatal("WebFetch alias should match fetch_url")
	}
	if ToolAllowed("write_file", []string{"Read"}) {
		t.Fatal("write_file must not be allowed when only Read is listed")
	}
}

func TestParseAllowedToolsSpaceSeparated(t *testing.T) {
	t.Parallel()
	got := parseAllowedTools("Read Bash(git:*) list_files")
	want := []string{"Read", "Bash(git:*)", "list_files"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
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

func TestDiscoverFirstRootWinsReportsRootAndShadow(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project")
	system := filepath.Join(root, "system")
	writeSkill(t, filepath.Join(project, "shared"), "shared", "project wins", "project")
	writeSkill(t, filepath.Join(system, "shared"), "shared", "system loses", "system")

	got, err := Loader{Dirs: []string{project, system}}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	entry := got[0]
	if entry.Name != "shared" {
		t.Fatalf("Name = %q, want shared", entry.Name)
	}
	if entry.Description != "project wins" {
		t.Fatalf("Description = %q, want the winning root's description", entry.Description)
	}
	if entry.Root != project {
		t.Fatalf("Root = %q, want %q (first root wins)", entry.Root, project)
	}
	wantShadow := filepath.Join(system, "shared")
	if len(entry.Shadowed) != 1 || entry.Shadowed[0] != wantShadow {
		t.Fatalf("Shadowed = %v, want [%q]", entry.Shadowed, wantShadow)
	}
}

func TestDiscoverFollowsSkillDirectorySymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	writeSkill(t, real, "linked", "via symlink", "body")
	link := filepath.Join(root, "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	scan := t.TempDir()
	if err := os.Symlink(link, filepath.Join(scan, "linked")); err != nil {
		t.Fatal(err)
	}

	got, err := Loader{Dir: scan}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "linked" {
		t.Fatalf("Discover() = %v, want the symlinked skill dir accepted", got)
	}
}

func TestDiscoverSkipsHiddenDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, ".git"), "hidden", "must be skipped", "body")

	got, err := Loader{Dir: dir}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover() = %v, want hidden dir skipped", got)
	}
}

func TestDiscoverSkipsMalformedFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	broken := filepath.Join(dir, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("no frontmatter here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(dir, "ok"), "ok", "fine", "body")

	got, err := Loader{Dir: dir}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("Discover() = %v, want only the well-formed skill", got)
	}
}

func TestDiscoverMissingRootIsNotAnError(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := Loader{Dir: missing}.Discover(context.Background())
	if err != nil {
		t.Fatalf("missing root: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing root: got %v, want none", got)
	}
}

func TestDiscoverMarksDisabledCaseInsensitiveTrimmed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "calibre"), "calibre", "library search", "body")
	writeSkill(t, filepath.Join(dir, "other"), "other", "unaffected", "body")

	got, err := Loader{Dir: dir, Disabled: []string{"  Calibre  "}}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	byName := map[string]Discovered{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if !byName["calibre"].Disabled {
		t.Fatalf("calibre.Disabled = false, want true (case/whitespace-insensitive match)")
	}
	if byName["other"].Disabled {
		t.Fatal("other.Disabled = true, want false (not in Disabled list)")
	}
}

func TestCatalogOmitsDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "calibre"), "calibre", "library search", "body")
	writeSkill(t, filepath.Join(dir, "other"), "other", "unaffected", "body")

	got, err := Loader{Dir: dir, Disabled: []string{"calibre"}}.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "other" {
		t.Fatalf("Catalog() = %v, want only the non-disabled skill", names(got))
	}
}

// TestCatalogEqualsDiscoverMinusProvenance guards the refactor's whole
// point: Catalog is Discover reduced to the Skill field, nothing more.
func TestCatalogEqualsDiscoverMinusProvenance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project")
	system := filepath.Join(root, "system")
	writeSkill(t, filepath.Join(project, "shared"), "shared", "project wins", "project")
	writeSkill(t, filepath.Join(system, "shared"), "shared", "system loses", "system")
	writeSkill(t, filepath.Join(system, "only-system"), "only-system", "system only", "sys")

	loader := Loader{Dirs: []string{project, system}}
	discovered, err := loader.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	catalog, err := loader.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(catalog) != len(discovered) {
		t.Fatalf("Catalog() has %d entries, Discover() has %d", len(catalog), len(discovered))
	}
	for i, d := range discovered {
		if !reflect.DeepEqual(catalog[i], d.Skill) {
			t.Fatalf("Catalog()[%d] = %+v, want %+v", i, catalog[i], d.Skill)
		}
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
		filepath.Join(work, ".agents", "skills"),
		filepath.Join(home, ".agents", "skills"),
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

	// Must not include Claude-native paths (Claude harness owns those).
	for _, p := range got {
		if strings.Contains(p, ".claude") {
			t.Fatalf("Ollama must not scan .claude skills: %q", p)
		}
	}

	// Empty workDir still includes user + configured.
	got = DefaultSearchPaths("", configured)
	if len(got) != 2 || got[0] != filepath.Join(home, ".agents", "skills") || got[1] != filepath.Clean(configured) {
		t.Fatalf("empty workDir paths = %v", got)
	}
}

func TestDefaultSearchPathsIncludesAncestorAgentSkills(t *testing.T) {
	// Not parallel: overrides package userHomeDir.
	home := t.TempDir()
	restore := SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)

	campaignRoot := t.TempDir()
	workDir := filepath.Join(campaignRoot, "projects", "samantha")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(campaignRoot, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	configured := filepath.Join(campaignRoot, "configured-skills")
	got := DefaultSearchPaths(workDir, configured)
	want := []string{
		filepath.Join(workDir, ".agents", "skills"),
		filepath.Join(campaignRoot, ".agents", "skills"),
		filepath.Join(home, ".agents", "skills"),
		configured,
	}
	if len(got) != len(want) {
		t.Fatalf("DefaultSearchPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if gotAncestor := ancestorAgentSkillsDir(workDir); gotAncestor != filepath.Join(campaignRoot, ".agents", "skills") {
		t.Fatalf("ancestorAgentSkillsDir = %q, want %q", gotAncestor, filepath.Join(campaignRoot, ".agents", "skills"))
	}
}

func TestAncestorAgentSkillsDirReturnsEmptyOutsideWorkspace(t *testing.T) {
	if got := ancestorAgentSkillsDir(t.TempDir()); got != "" {
		t.Fatalf("ancestorAgentSkillsDir outside workspace = %q, want empty", got)
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
