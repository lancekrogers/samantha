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

// TestSkillsDisableEnableConfigRoundTrip covers the DoD's core requirement:
// disable/enable persist through config.SetAndSave (not ValidateAndSet, which
// would reject a list) and round-trip through a real config.Load(); both
// verbs are idempotent; disable refuses an undiscovered name with the roots.
func TestSkillsDisableEnableConfigRoundTrip(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	fakeHome := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(restore)

	workDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(workDir, ".agents", "skills", "calibre"), "calibre", "desc")

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	// Each invocation reloads from disk, exactly like a fresh CLI process
	// would — this is what makes the round trip real rather than in-memory.
	loadConfig := config.Load

	run := func(args ...string) (skillsToggleResult, error) {
		cmd := newSkillsCmd(loadConfig)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			return skillsToggleResult{}, err
		}
		var result skillsToggleResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode result: %v\noutput: %s", err, out.String())
		}
		return result, nil
	}

	// Unknown name: disable refuses with the roots, exits non-zero.
	_, err = run("disable", "nope", "--json")
	if err == nil {
		t.Fatal("disable of an undiscovered name must fail")
	}
	if !strings.Contains(err.Error(), `no skill named "nope" was discovered (roots:`) {
		t.Fatalf("error = %q, want the roots-listing refusal", err)
	}

	// Disable a real, discovered skill.
	result, err := run("disable", "calibre", "--json")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if result.Name != "calibre" || !result.Disabled || result.Active || !result.RestartRequired {
		t.Fatalf("disable result = %+v", result)
	}
	if len(result.SkillsDisabled) != 1 || result.SkillsDisabled[0] != "calibre" {
		t.Fatalf("skills_disabled = %v, want [calibre]", result.SkillsDisabled)
	}

	// Round trip: a fresh config.Load() sees the persisted key.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.SkillsDisabled) != 1 || reloaded.SkillsDisabled[0] != "calibre" {
		t.Fatalf("reloaded SkillsDisabled = %v, want [calibre]", reloaded.SkillsDisabled)
	}

	// Idempotent: disabling an already-disabled skill succeeds, no duplicate.
	result, err = run("disable", "Calibre", "--json")
	if err != nil {
		t.Fatalf("idempotent disable: %v", err)
	}
	if len(result.SkillsDisabled) != 1 {
		t.Fatalf("re-disable duplicated the entry: %v", result.SkillsDisabled)
	}

	// Enable removes it; round-trips back to empty.
	result, err = run("enable", "calibre", "--json")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if result.Disabled || !result.Active {
		t.Fatalf("enable result = %+v, want disabled=false active=true", result)
	}
	if len(result.SkillsDisabled) != 0 {
		t.Fatalf("skills_disabled after enable = %v, want empty", result.SkillsDisabled)
	}
	reloaded, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.SkillsDisabled) != 0 {
		t.Fatalf("reloaded after enable = %v, want empty", reloaded.SkillsDisabled)
	}

	// Idempotent: enabling a name that was never disabled is a no-op success,
	// even though its folder is real — and also for a name whose folder is
	// gone entirely.
	result, err = run("enable", "calibre", "--json")
	if err != nil {
		t.Fatalf("enable no-op: %v", err)
	}
	if result.Disabled || len(result.SkillsDisabled) != 0 {
		t.Fatalf("no-op enable result = %+v", result)
	}
	result, err = run("enable", "ghost-folder-gone", "--json")
	if err != nil {
		t.Fatalf("enable of a name with no folder must still succeed: %v", err)
	}
	if result.Disabled {
		t.Fatalf("enable result = %+v, want disabled=false", result)
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
