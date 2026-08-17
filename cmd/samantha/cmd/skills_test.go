package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/skills"
)

func skillsTestConfig(cfg *config.Config) func() (*config.Config, error) {
	return func() (*config.Config, error) {
		return cfg, nil
	}
}

func writeSkillFixture(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsListJSONEmptyDiscoveryPrintsEmptySkillsArray(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	cfg := &config.Config{SkillsEnabled: true, BrainProvider: "ollama", SkillsDir: filepath.Join(t.TempDir(), "missing")}

	cmd := newSkillsCmd(skillsTestConfig(cfg))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json", "--work-dir", workDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("skills list --json: %v", err)
	}

	var env skillsListEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\noutput: %s", err, out.String())
	}
	if env.Schema != skillsSchema {
		t.Fatalf("schema = %q, want %q", env.Schema, skillsSchema)
	}
	if env.Skills == nil || len(env.Skills) != 0 {
		t.Fatalf("skills = %v, want non-nil empty slice", env.Skills)
	}
	// Confirm the raw JSON says [] and not null.
	if !strings.Contains(out.String(), `"skills": []`) {
		t.Fatalf("output must contain an empty skills array literal, got: %s", out.String())
	}
	// Workspace-ancestor .agents/skills only appears as a distinct root when
	// a real ancestor has one; in an isolated temp dir it collapses with the
	// project root, so 3-4 entries are both valid depending on environment.
	if len(env.Roots) < 3 {
		t.Fatalf("roots = %v, want at least 3 entries (project, user, skills_dir)", env.Roots)
	}
	for _, r := range env.Roots {
		if r.Exists {
			t.Errorf("root %q exists = true, want false (nothing was created)", r.Path)
		}
	}
}

func TestSkillsListJSONEnvelopeShape(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(workDir, ".agents", "skills", "calibre"), "calibre", "Search the library.")

	cfg := &config.Config{
		SkillsEnabled:             true,
		BrainProvider:             "ollama",
		OllamaEmbeddingModel:      "nomic-embed-text",
		SkillsSimilarityThreshold: 0.55,
		SkillsDir:                 filepath.Join(t.TempDir(), "missing"),
	}

	cmd := newSkillsCmd(skillsTestConfig(cfg))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json", "--work-dir", workDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("skills list --json: %v", err)
	}

	var env skillsListEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\noutput: %s", err, out.String())
	}
	if env.WorkDir != workDir {
		t.Errorf("work_dir = %q, want %q", env.WorkDir, workDir)
	}
	if !env.SkillsEnabled {
		t.Error("skills_enabled = false, want true")
	}
	if env.BrainProvider != "ollama" {
		t.Errorf("brain_provider = %q, want ollama", env.BrainProvider)
	}
	if !env.ProviderSupported {
		t.Error("provider_supported = false, want true for ollama")
	}
	if env.EmbeddingModel != "nomic-embed-text" {
		t.Errorf("embedding_model = %q, want nomic-embed-text", env.EmbeddingModel)
	}
	if env.SimilarityThreshold != 0.55 {
		t.Errorf("similarity_threshold = %v, want 0.55", env.SimilarityThreshold)
	}
	if len(env.Skills) != 1 {
		t.Fatalf("skills = %v, want 1 entry", env.Skills)
	}
	row := env.Skills[0]
	if row.Name != "calibre" || row.Description != "Search the library." {
		t.Fatalf("row = %+v", row)
	}
	if row.Root != filepath.Join(workDir, ".agents", "skills") {
		t.Errorf("row.Root = %q, want the winning search root", row.Root)
	}
	if row.Path != filepath.Join(workDir, ".agents", "skills", "calibre") {
		t.Errorf("row.Path = %q, want the skill directory", row.Path)
	}
	if !row.Active {
		t.Error("row.Active = false, want true (enabled, supported provider, not disabled)")
	}
	if row.Disabled {
		t.Error("row.Disabled = true, want false")
	}
	if row.Shadowed == nil || len(row.Shadowed) != 0 {
		t.Errorf("row.Shadowed = %v, want empty (non-nil) slice", row.Shadowed)
	}

	// The winning root's count reflects what's actually in that folder.
	var projectRoot skillsRootRow
	for _, r := range env.Roots {
		if r.Path == filepath.Join(workDir, ".agents", "skills") {
			projectRoot = r
		}
	}
	if !projectRoot.Exists || projectRoot.Skills != 1 {
		t.Fatalf("project root = %+v, want exists=true skills=1", projectRoot)
	}
}

func TestSkillsListWorkDirFlagChangesRoots1And2(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workA := t.TempDir()
	workB := t.TempDir()
	writeSkillFixture(t, filepath.Join(workA, ".agents", "skills", "only-in-a"), "only-in-a", "lives in A")

	cfg := &config.Config{SkillsEnabled: true, BrainProvider: "ollama", SkillsDir: filepath.Join(t.TempDir(), "missing")}

	run := func(workDir string) skillsListEnvelope {
		cmd := newSkillsCmd(skillsTestConfig(cfg))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"list", "--json", "--work-dir", workDir})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("skills list --json --work-dir %s: %v", workDir, err)
		}
		var env skillsListEnvelope
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env
	}

	envA := run(workA)
	if len(envA.Skills) != 1 || envA.Skills[0].Name != "only-in-a" {
		t.Fatalf("work dir A skills = %v, want only-in-a", envA.Skills)
	}
	envB := run(workB)
	if len(envB.Skills) != 0 {
		t.Fatalf("work dir B skills = %v, want none", envB.Skills)
	}
}

func TestSkillsListActiveFalseWhenSkillsDisabledGlobally(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(workDir, ".agents", "skills", "calibre"), "calibre", "desc")

	cfg := &config.Config{SkillsEnabled: false, BrainProvider: "ollama", SkillsDir: filepath.Join(t.TempDir(), "missing")}
	env := runSkillsList(t, cfg, workDir)
	if len(env.Skills) != 1 {
		t.Fatalf("skills = %v, want 1 entry (discovery is independent of skills_enabled)", env.Skills)
	}
	if env.Skills[0].Active {
		t.Error("Active = true, want false when skills_enabled is globally off")
	}
}

func TestSkillsListActiveFalseWhenDisabledByName(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(workDir, ".agents", "skills", "calibre"), "calibre", "desc")

	cfg := &config.Config{
		SkillsEnabled:  true,
		BrainProvider:  "ollama",
		SkillsDisabled: []string{"calibre"},
		SkillsDir:      filepath.Join(t.TempDir(), "missing"),
	}
	env := runSkillsList(t, cfg, workDir)
	if len(env.Skills) != 1 {
		t.Fatalf("skills = %v, want 1 entry", env.Skills)
	}
	row := env.Skills[0]
	if !row.Disabled {
		t.Error("Disabled = false, want true")
	}
	if row.Active {
		t.Error("Active = true, want false when disabled by name")
	}
}

func TestSkillsListActiveFalseForUnsupportedProvider(t *testing.T) {
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(workDir, ".agents", "skills", "calibre"), "calibre", "desc")

	for _, provider := range []string{"claude", "grok"} {
		cfg := &config.Config{SkillsEnabled: true, BrainProvider: provider, SkillsDir: filepath.Join(t.TempDir(), "missing")}
		env := runSkillsList(t, cfg, workDir)
		if env.ProviderSupported {
			t.Errorf("provider %q: provider_supported = true, want false", provider)
		}
		if len(env.Skills) != 1 || env.Skills[0].Active {
			t.Errorf("provider %q: skills = %v, want discovered but not active", provider, env.Skills)
		}
	}
}

func runSkillsList(t *testing.T, cfg *config.Config, workDir string) skillsListEnvelope {
	t.Helper()
	cmd := newSkillsCmd(skillsTestConfig(cfg))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json", "--work-dir", workDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("skills list --json: %v", err)
	}
	var env skillsListEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\noutput: %s", err, out.String())
	}
	return env
}
