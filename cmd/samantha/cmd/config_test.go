package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

func runConfigMigrate(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	return runConfigMigrateWithPath(t, cfg, "/tmp/samantha/config.yaml", args...)
}

func runConfigMigrateWithPath(t *testing.T, cfg *config.Config, configPath string, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigMigrateCmd(func() (*config.Config, error) {
		if cfg == nil {
			return nil, errors.New("load failed")
		}
		return cfg, nil
	}, func() string { return configPath })

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestConfigMigrateDryRunOutput(t *testing.T) {
	out, err := runConfigMigrate(t, &config.Config{STTProvider: "sherpa-streaming"}, "--dry-run")
	if err != nil {
		t.Fatalf("config migrate --dry-run error = %v", err)
	}

	for _, want := range []string{
		"Config migration dry run",
		"config_path: /tmp/samantha/config.yaml",
		"current_alias: sherpa-streaming",
		"proposed_stt_provider: sherpa",
		"proposed_stt_mode: streaming",
		"no_op: false",
		"would_write: false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestConfigMigrateRequiresDryRun(t *testing.T) {
	_, err := runConfigMigrate(t, &config.Config{STTProvider: "sherpa"})
	if err == nil {
		t.Fatal("config migrate error = nil, want mode requirement")
	}
	if !strings.Contains(err.Error(), "requires exactly one") {
		t.Fatalf("error = %q, want exactly-one mode requirement", err)
	}
}

func TestConfigMigrateDryRunUnknownProvider(t *testing.T) {
	_, err := runConfigMigrate(t, &config.Config{STTProvider: "google"}, "--dry-run")
	if err == nil {
		t.Fatal("config migrate --dry-run error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported stt_provider") {
		t.Fatalf("error = %q, want unsupported provider", err)
	}
}

func TestConfigMigrateWriteOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("stt_provider: sherpa-offline\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runConfigMigrateWithPath(t, &config.Config{STTProvider: "sherpa-offline"}, path, "--write")
	if err != nil {
		t.Fatalf("config migrate --write error = %v", err)
	}
	for _, want := range []string{
		"Config migration write",
		"current_alias: sherpa-offline",
		"proposed_stt_provider: sherpa",
		"proposed_stt_mode: offline",
		"no_op: false",
		"wrote: true",
		"backup_path: " + path + ".bak.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "stt_provider: sherpa") || !strings.Contains(string(written), "stt_mode: offline") {
		t.Fatalf("written config = %q, want explicit stt provider/mode", written)
	}
}

func TestShouldSeedPromptsSkipsConfigMigrate(t *testing.T) {
	root := &cobra.Command{Use: "samantha"}
	configCmd := &cobra.Command{Use: "config"}
	migrateCmd := &cobra.Command{Use: "migrate"}
	configCmd.AddCommand(migrateCmd)
	root.AddCommand(configCmd)

	if shouldSeedPrompts(migrateCmd) {
		t.Fatal("shouldSeedPrompts(config migrate) = true, want false")
	}
	if !shouldSeedPrompts(configCmd) {
		t.Fatal("shouldSeedPrompts(config) = false, want true")
	}
}
