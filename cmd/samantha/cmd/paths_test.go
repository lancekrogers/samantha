package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func runPathsCmd(t *testing.T, report pathsReport, asJSON bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runPaths(cmd, report, asJSON); err != nil {
		t.Fatalf("runPaths(asJSON=%v) error = %v", asJSON, err)
	}
	return buf.String()
}

// TestCollectPathsNoEmptyValues guards against a helper regressing to "":
// every reported path must resolve even on a pristine install.
func TestCollectPathsNoEmptyValues(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())

	data, err := json.Marshal(collectPaths())
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	for key, val := range got {
		if s, ok := val.(string); ok && s == "" {
			t.Errorf("collectPaths returned empty %s", key)
		}
	}
}

// TestPathsJSONContract locks the machine-readable shape the Obey Voice Mac
// app parses (WI-afaf92): exact key set, schema version, and values derived
// from the centralized install-path helpers.
func TestPathsJSONContract(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)

	out := runPathsCmd(t, collectPaths(), true)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("paths --json is not valid JSON: %v\n%s", err, out)
	}

	wantKeys := []string{
		"schema_version", "app_slug", "install_root", "config_file",
		"serve_dir", "cache_dir", "models_dir", "sessions_dir",
		"meetings_dir", "personas_dir", "prompts_dir", "skills_dir",
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("paths --json missing key %q", k)
		}
	}
	if len(got) != len(wantKeys) {
		t.Errorf("paths --json has %d keys, want %d — additions must extend wantKeys, renames must bump schema_version:\n%s", len(got), len(wantKeys), out)
	}

	fixed := map[string]any{
		"schema_version": float64(pathsSchemaVersion),
		"app_slug":       config.AppSlug,
		"install_root":   dir,
		"config_file":    filepath.Join(dir, "config.yaml"),
		"serve_dir":      filepath.Join(dir, "serve"),
		"sessions_dir":   filepath.Join(dir, "sessions"),
		"personas_dir":   filepath.Join(dir, "personas"),
	}
	for k, want := range fixed {
		if got[k] != want {
			t.Errorf("paths --json %s = %v, want %v", k, got[k], want)
		}
	}
}

func TestPathsHumanListsAllPaths(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)

	out := runPathsCmd(t, collectPaths(), false)

	for _, want := range []string{
		"Samantha Paths",
		config.AppSlug,
		dir,
		filepath.Join(dir, "serve"),
		filepath.Join(dir, "config.yaml"),
	} {
		if !contains(out, want) {
			t.Errorf("paths output missing %q:\n%s", want, out)
		}
	}
}
