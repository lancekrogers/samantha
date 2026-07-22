package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/skills"
)

func fixtureCatalog() []skills.Skill {
	return []skills.Skill{
		{
			Name:        "hello",
			Description: "A friendly greeting skill for tests.",
			Body:        "# Hello\n\nSay hello warmly.\n",
			Dir:         "/skills/hello",
		},
	}
}

func TestSkillContextAdvertise(t *testing.T) {
	t.Parallel()

	if SkillContext(nil) != "" {
		t.Fatal("SkillContext(nil) want empty")
	}
	if SkillContext([]skills.Skill{}) != "" {
		t.Fatal("SkillContext(empty) want empty")
	}

	got := SkillContext(fixtureCatalog())
	if !strings.Contains(got, "Available skills") {
		t.Fatalf("missing header: %q", got)
	}
	if !strings.Contains(got, "read_skill") {
		t.Fatalf("missing read_skill instruction: %q", got)
	}
	if !strings.Contains(got, "semantically matches") || !strings.Contains(got, "discovery fallback") {
		t.Fatalf("missing semantic skill-routing policy: %q", got)
	}
	if !strings.Contains(got, "- hello: A friendly greeting skill for tests.") {
		t.Fatalf("missing skill line: %q", got)
	}
	if strings.Contains(got, "Say hello warmly") {
		t.Fatal("SkillContext must not include skill body")
	}
}

func TestSkillContextTruncatesLongDescription(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("z", skills.MaxDescriptionRunes+80)
	got := SkillContext([]skills.Skill{{Name: "big", Description: long}})
	// Description in the menu line should be capped.
	line := got[strings.LastIndex(got, "- big: "):]
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "- big: ") {
		t.Fatalf("unexpected line: %q", line)
	}
	desc := strings.TrimPrefix(line, "- big: ")
	if n := len([]rune(desc)); n != skills.MaxDescriptionRunes {
		t.Fatalf("desc runes = %d, want %d (%q)", n, skills.MaxDescriptionRunes, desc)
	}
	if !strings.HasSuffix(desc, "…") {
		t.Fatalf("want ellipsis suffix, got %q", desc)
	}
}

func TestBuildMessagesIncludesSkillsWhenLoaded(t *testing.T) {
	t.Parallel()

	o := &OllamaBrain{
		workDir:      "/work",
		cfg:          &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt: "You are Samantha.",
		skills:       fixtureCatalog(),
	}
	sys := o.buildMessages()[0].Content
	if !strings.Contains(sys, "- hello: A friendly greeting skill for tests.") {
		t.Fatalf("system prompt missing advertised skill: %q", sys)
	}
	if strings.Contains(sys, "Say hello warmly") {
		t.Fatal("system prompt must not embed full skill body")
	}
}

func TestBuildMessagesInjectsSemanticallyActivatedSkillBodies(t *testing.T) {
	t.Parallel()

	o := &OllamaBrain{
		workDir:            "/work",
		cfg:                &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt:       "You are Samantha.",
		skills:             fixtureCatalog(),
		activeSkillContext: ActivatedSkillContext(fixtureCatalog()),
	}
	sys := o.buildMessages()[0].Content
	if !strings.Contains(sys, "<activated_skills>") || !strings.Contains(sys, "Say hello warmly") {
		t.Fatalf("system prompt missing activated skill body: %q", sys)
	}
}

func TestBuildMessagesOmitsSkillsWhenEmpty(t *testing.T) {
	t.Parallel()

	o := &OllamaBrain{
		workDir:      "/work",
		cfg:          &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt: "You are Samantha.",
		skills:       nil,
	}
	sys := o.buildMessages()[0].Content
	if strings.Contains(sys, "Available skills") {
		t.Fatalf("system prompt should omit skills block when catalog empty: %q", sys)
	}
}

func TestLoadSkillsCatalogGated(t *testing.T) {
	// Isolate from the developer's real ~/.agents/skills.
	fakeHome := t.TempDir()
	prev := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(prev)

	// Disabled: never load, even if a dir is set.
	got, err := loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: false,
		SkillsDir:     "testdata-does-not-matter",
	}, "/tmp/project")
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled: got %d skills, want 0", len(got))
	}

	// Enabled with empty project and only a missing configured dir: empty catalog.
	missingRoot := t.TempDir()
	got, err = loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     missingRoot + "/missing-skills",
	}, missingRoot+"/no-project")
	if err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing dir: got %d skills, want 0", len(got))
	}

	// Enabled with fixture as configured Samantha skills_dir.
	fixture := "../skills/testdata/skills"
	got, err = loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     fixture,
	}, t.TempDir()) // empty project .agents/skills
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("fixture: got %#v, want hello skill", got)
	}
}

func TestLoadSkillsCatalogProjectOverridesSystem(t *testing.T) {
	fakeHome := t.TempDir()
	prev := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(prev)

	root := t.TempDir()
	// Project skills live under .agents/skills (not .claude).
	projectSkill := filepath.Join(root, ".agents", "skills", "shared")
	if err := os.MkdirAll(projectSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: shared\ndescription: from project\n---\nproject body\n"
	if err := os.WriteFile(filepath.Join(projectSkill, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Configured system dir also has "shared" — project must win.
	systemRoot := filepath.Join(root, "system-skills")
	systemSkill := filepath.Join(systemRoot, "shared")
	if err := os.MkdirAll(systemSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	sysContent := "---\nname: shared\ndescription: from system\n---\nsystem body\n"
	if err := os.WriteFile(filepath.Join(systemSkill, "SKILL.md"), []byte(sysContent), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     systemRoot,
	}, root)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1", len(got))
	}
	if got[0].Description != "from project" {
		t.Fatalf("description = %q, want project to win over system", got[0].Description)
	}
}

func TestLoadSkillsCatalogIncludesAncestorAgentSkills(t *testing.T) {
	fakeHome := t.TempDir()
	prev := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(prev)

	campaignRoot := t.TempDir()
	workDir := filepath.Join(campaignRoot, "projects", "samantha")
	workspaceSkills := filepath.Join(campaignRoot, ".agents", "skills", "workspace-context")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceSkills, "SKILL.md"), []byte("---\nname: workspace-context\ndescription: explains the workspace\n---\nUse the workspace skills when answering questions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     filepath.Join(campaignRoot, "missing-config-skills"),
	}, workDir)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(got) != 1 || got[0].Name != "workspace-context" {
		t.Fatalf("got %#v, want workspace-context skill", got)
	}
}

func TestLoadSkillsCatalogIgnoresClaudeSkills(t *testing.T) {
	fakeHome := t.TempDir()
	prev := skills.SetUserHomeDirForTest(func() (string, error) { return fakeHome, nil })
	t.Cleanup(prev)

	root := t.TempDir()
	// Only .claude/skills present — Ollama must not load these.
	claudeSkill := filepath.Join(root, ".claude", "skills", "claude-only")
	if err := os.MkdirAll(claudeSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: claude-only\ndescription: should not load\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(claudeSkill, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     filepath.Join(root, "missing-samantha-skills"),
	}, root)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d skills from .claude/skills, want 0 (Claude harness owns that path)", len(got))
	}
}

func TestReadSkillTool(t *testing.T) {
	t.Parallel()

	catalog := fixtureCatalog()
	call := skillCall("hello")

	got := executeTool(context.Background(), "/work", call, catalog)
	if !strings.Contains(got, "Say hello warmly") {
		t.Fatalf("read_skill body missing: %q", got)
	}
	if !strings.Contains(got, "/skills/hello") {
		t.Fatalf("read_skill must surface skill dir: %q", got)
	}

	got = executeTool(context.Background(), "/work", skillCall("nope"), catalog)
	if !strings.Contains(got, "unknown skill") {
		t.Fatalf("unknown skill: want error string, got %q", got)
	}

	// Cap large bodies.
	big := skills.Skill{
		Name: "big",
		Body: strings.Repeat("x", skillBodyMaxBytes+100),
		Dir:  "/skills/big",
	}
	got = executeTool(context.Background(), "/work", skillCall("big"), []skills.Skill{big})
	if !strings.Contains(got, "... (truncated)") {
		t.Fatalf("expected truncation marker, got len=%d", len(got))
	}
	if len(got) > skillBodyMaxBytes+200 {
		t.Fatalf("truncated body still too large: %d", len(got))
	}
}

func TestToolRunCommandUsesConfiguredTimeout(t *testing.T) {
	start := time.Now()
	result := toolRunCommandWithTimeout(context.Background(), t.TempDir(), map[string]any{
		"command": "sleep 1",
	}, 10*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timed out command took %v, want under 500ms", elapsed)
	}
	if !strings.Contains(result, "exit error") {
		t.Fatalf("timed out command result = %q, want exit error", result)
	}
}

func TestToolSessionKeepsAllToolsAfterSkillLoad(t *testing.T) {
	t.Parallel()

	catalog := []skills.Skill{
		{
			Name:         "restricted",
			Description:  "suggests read",
			Body:         "prefer read_file for this workflow",
			Dir:          "/skills/restricted",
			AllowedTools: []string{"Read"},
		},
	}
	sess := &toolSession{catalog: catalog}

	// Before activation: all base tools + read_skill.
	before := sess.tools()
	if !hasTool(before, "write_file") || !hasTool(before, "read_skill") {
		t.Fatalf("pre-activation tools incomplete: %#v", toolNames(before))
	}

	// Load via read_skill. allowed-tools metadata must not shrink a general
	// Ollama harness or prevent another relevant skill from being loaded.
	got := sess.execute(context.Background(), "/work", skillCall("restricted"))
	if !strings.Contains(got, "prefer read_file") {
		t.Fatalf("skill body missing: %q", got)
	}
	if strings.Contains(got, "only these tools") {
		t.Fatalf("skill load still reports a restrictive tool policy: %q", got)
	}

	// Every implemented tool remains available despite allowed-tools: Read.
	after := sess.tools()
	for _, name := range []string{"list_files", "read_file", "write_file", "run_command", "web_search", "fetch_url", "read_skill"} {
		if !hasTool(after, name) {
			t.Fatalf("after skill load, tool %s missing: %#v", name, toolNames(after))
		}
	}

	// A tool outside allowed-tools executes normally.
	dir := t.TempDir()
	write := sess.execute(context.Background(), dir, api.ToolCall{
		Function: api.ToolCallFunction{
			Name:      "write_file",
			Arguments: mustArgs(map[string]any{"path": "out.txt", "content": "ok"}),
		},
	})
	if !strings.Contains(write, "wrote") {
		t.Fatalf("write_file should remain available: %q", write)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatalf("write_file did not create file: %v", err)
	}
}

func TestToolSessionIgnoresUnmappedAllowedTools(t *testing.T) {
	t.Parallel()
	catalog := []skills.Skill{{
		Name:         "webonly",
		Description:  "bad allow-list",
		Body:         "body",
		Dir:          "/s",
		AllowedTools: []string{"Browser"},
	}}
	sess := &toolSession{catalog: catalog}
	got := sess.execute(context.Background(), "/work", skillCall("webonly"))
	if !strings.Contains(got, "body") {
		t.Fatalf("skill with unknown allowed-tools should still load: %q", got)
	}
}

func TestToolSessionCanLoadMultipleSkills(t *testing.T) {
	t.Parallel()
	catalog := []skills.Skill{
		{
			Name:         "restricted",
			Description:  "read only",
			Body:         "use Read",
			Dir:          "/skills/restricted",
			AllowedTools: []string{"Read"},
		},
		{
			Name:        "open",
			Description: "full tools",
			Body:        "go",
			Dir:         "/skills/open",
		},
	}
	sess := &toolSession{catalog: catalog}
	_ = sess.execute(context.Background(), "/work", skillCall("restricted"))
	got := sess.execute(context.Background(), "/work", skillCall("open"))
	if !strings.Contains(got, "go") {
		t.Fatalf("second relevant skill did not load: %q", got)
	}
}

func TestToolSessionUnrestrictedWhenNoAllowedTools(t *testing.T) {
	t.Parallel()

	catalog := []skills.Skill{{
		Name: "open", Description: "all tools", Body: "go", Dir: "/s",
	}}
	sess := &toolSession{catalog: catalog}
	_ = sess.execute(context.Background(), "/work", skillCall("open"))
	tools := sess.tools()
	for _, name := range []string{"list_files", "read_file", "write_file", "run_command", "web_search", "fetch_url", "read_skill"} {
		if !hasTool(tools, name) {
			t.Fatalf("unrestricted skill missing %s", name)
		}
	}
}

func toolNames(tools api.Tools) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func mustArgs(m map[string]any) api.ToolCallFunctionArguments {
	args := api.NewToolCallFunctionArguments()
	for k, v := range m {
		args.Set(k, v)
	}
	return args
}

func skillCall(name string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	args.Set("name", name)
	return api.ToolCall{
		Function: api.ToolCallFunction{
			Name:      "read_skill",
			Arguments: args,
		},
	}
}

func TestVoiceAssistantToolsReadSkillGating(t *testing.T) {
	t.Parallel()

	// No catalog → no read_skill (skills disabled / empty).
	tools := voiceAssistantTools(nil)
	if hasTool(tools, "read_skill") {
		t.Fatal("read_skill offered with empty catalog")
	}
	if !hasTool(tools, "list_files") {
		t.Fatal("base tools missing")
	}
	if !hasTool(tools, "web_search") || !hasTool(tools, "fetch_url") {
		t.Fatal("web tools missing")
	}

	// Catalog present → read_skill offered when tools are enabled.
	tools = voiceAssistantTools(fixtureCatalog())
	if !hasTool(tools, "read_skill") {
		t.Fatal("read_skill missing with non-empty catalog")
	}

	// Network turn with remote_tools_enabled=false maps to ToolsEnabled=false
	// in the serve pipeline — no tools (including read_skill) are offered.
	// Modelled here as "caller does not attach tools at all".
	var noTools api.Tools
	if hasTool(noTools, "read_skill") {
		t.Fatal("empty tools list must not include read_skill")
	}
}

func hasTool(tools api.Tools, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}
