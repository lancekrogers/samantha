package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

func runRootForPrompts(t *testing.T, promptDir string, args ...string) (string, error) {
	t.Helper()
	// Isolate from the real ~/.obey. `prompts show persona` calls config.Load,
	// whose afterLoad hook (persona.EnsureAndApply) overlays the machine's active
	// persona onto cfg.Persona. A fresh temp config dir means an empty personas
	// tree that seeds the embedded "samantha" default, and the active_persona
	// override defeats any value viper's config layer retained from a prior
	// test's real Load. Without both, the developer's real active persona leaks
	// in and flakes the "samantha"/"embedded" assertions in full-suite runs.
	config.SetConfigDirForTest(t, t.TempDir())
	config.Set("prompts_dir", promptDir)
	config.Set("persona", "samantha")
	config.Set("active_persona", "samantha")
	promptsListJSON = false
	promptsShowJSON = false
	t.Cleanup(func() {
		config.Set("prompts_dir", "")
		config.Set("persona", "samantha")
		config.Set("active_persona", "samantha")
		promptsListJSON = false
		promptsShowJSON = false
	})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	// cobra's --help is a persistent flag on the shared global rootCmd, and pflag
	// leaves a flag unset by the current args at its prior value. Clear it so a
	// preceding `prompts list --help` test can't make this Execute print help
	// instead of running the command (surfaces under `go test -count>1`).
	resetHelpFlags(rootCmd)
	err := rootCmd.Execute()
	return buf.String(), err
}

// resetHelpFlags clears any help flag left set (value + Changed) on cmd and its
// subcommands so the reused global rootCmd starts each Execute clean.
func resetHelpFlags(cmd *cobra.Command) {
	if f := cmd.Flags().Lookup("help"); f != nil {
		_ = f.Value.Set("false")
		f.Changed = false
	}
	for _, sub := range cmd.Commands() {
		resetHelpFlags(sub)
	}
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

func TestPromptsListIncludesUserDocuments(t *testing.T) {
	promptDir := t.TempDir()
	styleDir := filepath.Join(promptDir, "style")
	if err := os.MkdirAll(styleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(styleDir, "casual.md"), []byte("Speak casually.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRootForPrompts(t, promptDir, "prompts", "list", "--json")
	if err != nil {
		t.Fatalf("prompts list error = %v", err)
	}
	var entries []prompts.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("prompts list --json output is not valid JSON: %v\n%s", err, out)
	}
	found := false
	for _, e := range entries {
		if e.Kind == prompts.KindStyle && e.Name == "casual" {
			found = true
			if e.Source != prompts.SourceUser {
				t.Errorf("style source = %s, want user", e.Source)
			}
			if e.Hash == "" {
				t.Error("style hash empty")
			}
		}
	}
	if !found {
		t.Fatalf("prompts list missing user style document; entries = %+v", entries)
	}
}

func TestPromptsCommandsHumanOutput(t *testing.T) {
	promptDir := t.TempDir()

	out, err := runRootForPrompts(t, promptDir, "prompts", "list")
	if err != nil {
		t.Fatalf("prompts list error = %v", err)
	}
	for _, want := range []string{"KIND", "NAME", "SOURCE", "HASH", "persona", "samantha", "embedded"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompts list output = %q, want it to contain %q", out, want)
		}
	}

	out, err = runRootForPrompts(t, promptDir, "prompts", "show", "persona")
	if err != nil {
		t.Fatalf("prompts show error = %v", err)
	}
	for _, want := range []string{"kind:   persona", "name:   samantha", "source: embedded", "hash:", "You are Samantha"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompts show output = %q, want it to contain %q", out, want)
		}
	}
}

func TestPromptsListHelpMatchesCatalogBehavior(t *testing.T) {
	out, err := runRootForPrompts(t, t.TempDir(), "prompts", "list", "--help")
	if err != nil {
		t.Fatalf("prompts list --help error = %v", err)
	}
	if !strings.Contains(out, "List embedded and user prompt documents") {
		t.Errorf("prompts list --help output = %q, want current catalog behavior", out)
	}
}
