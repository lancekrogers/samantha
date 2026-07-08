package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

func runRootForPrompts(t *testing.T, promptDir string, args ...string) (string, error) {
	t.Helper()
	config.Set("prompts_dir", promptDir)
	config.Set("persona", "samantha")
	t.Cleanup(func() {
		config.Set("prompts_dir", "")
		config.Set("persona", "samantha")
	})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestPromptsCommandsReportSeededDefaultsAsEmbedded(t *testing.T) {
	promptDir := t.TempDir()

	out, err := runRootForPrompts(t, promptDir, "prompts", "list", "--json")
	if err != nil {
		t.Fatalf("prompts list error = %v", err)
	}
	var entries []prompts.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("prompts list --json output is not valid JSON: %v\n%s", err, out)
	}
	for _, e := range entries {
		if e.Source != prompts.SourceEmbedded {
			t.Errorf("list kind %s source = %s, want embedded for seeded default", e.Kind, e.Source)
		}
		if e.Path != "" {
			t.Errorf("list kind %s path = %q, want empty for seeded default", e.Kind, e.Path)
		}
	}

	out, err = runRootForPrompts(t, promptDir, "prompts", "show", "persona", "--json")
	if err != nil {
		t.Fatalf("prompts show error = %v", err)
	}
	var shown struct {
		Source prompts.Source `json:"source"`
		Path   string         `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("prompts show --json output is not valid JSON: %v\n%s", err, out)
	}
	if shown.Source != prompts.SourceEmbedded {
		t.Errorf("show source = %s, want embedded for seeded default", shown.Source)
	}
	if shown.Path != "" {
		t.Errorf("show path = %q, want empty for seeded default", shown.Path)
	}
}
