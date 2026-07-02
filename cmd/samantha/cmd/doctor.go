package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

var doctorJSON bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose local setup: config, model assets, and external binaries (read-only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runDoctor(cmd, cfg, config.ModelsDir(), exec.LookPath, doctorJSON)
	},
}

var severityMark = map[config.Severity]string{
	config.SeverityOK:    "OK  ",
	config.SeverityWarn:  "WARN",
	config.SeverityError: "FAIL",
}

// runDoctor prints read-only setup diagnostics and returns an error (non-zero
// exit) only when a check has error severity. lookPath is injectable for tests.
func runDoctor(cmd *cobra.Command, cfg *config.Config, modelsDir string, lookPath func(string) (string, error), asJSON bool) error {
	diags := config.Diagnose(cfg, modelsDir, lookPath)
	out := cmd.OutOrStdout()

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(diags); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "\n  Samantha Doctor (models dir: %s)\n\n", modelsDir)
		for _, d := range diags {
			fmt.Fprintf(out, "  [%s] %s: %s\n", severityMark[d.Severity], d.Name, d.Detail)
			if d.Remediation != "" {
				fmt.Fprintf(out, "         → %s\n", d.Remediation)
			}
		}
		fmt.Fprintln(out)
	}

	if config.HasErrors(diags) {
		return fmt.Errorf("doctor found setup errors; see remediation above")
	}
	return nil
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Output machine-readable JSON")
	rootCmd.AddCommand(doctorCmd)
}
