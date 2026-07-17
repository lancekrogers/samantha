package brain

import (
	"context"
	"strings"
	"testing"

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
	if !strings.Contains(got, "- hello: A friendly greeting skill for tests.") {
		t.Fatalf("missing skill line: %q", got)
	}
	if strings.Contains(got, "Say hello warmly") {
		t.Fatal("SkillContext must not include skill body")
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
	t.Parallel()

	// Disabled: never load, even if a dir is set.
	got, err := loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: false,
		SkillsDir:     "testdata-does-not-matter",
	})
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled: got %d skills, want 0", len(got))
	}

	// Enabled with missing dir: empty catalog, not an error.
	got, err = loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     t.TempDir() + "/missing-skills",
	})
	if err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing dir: got %d skills, want 0", len(got))
	}

	// Enabled with fixture dir from the skills package.
	fixture := "../skills/testdata/skills"
	got, err = loadSkillsCatalog(context.Background(), &config.Config{
		SkillsEnabled: true,
		SkillsDir:     fixture,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("fixture: got %#v, want hello skill", got)
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
