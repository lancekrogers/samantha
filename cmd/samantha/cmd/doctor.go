package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

var (
	doctorJSON         bool
	doctorVoiceDevices bool
)

// voiceDeviceProbeTimeout bounds the opt-in hardware probe so a wedged audio
// backend cannot hang doctor.
const voiceDeviceProbeTimeout = 3 * time.Second

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose local setup: config, model assets, and external binaries (read-only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var checker config.VoiceDeviceChecker
		if doctorVoiceDevices {
			checker = audio.NewDeviceChecker()
		}
		// Bundle-aware path resolution finds calibredb in the macOS app bundle
		// when it is not on PATH; other binaries still use exec.LookPath.
		return runDoctor(cmd, cfg, config.ModelsDir(), doctorLookPath, checker, doctorJSON)
	},
}

// doctorLookPath resolves external tools for doctor. calibredb/ebook-convert use
// Calibre's bundle-aware lookup; everything else uses PATH.
func doctorLookPath(name string) (string, error) {
	switch name {
	case "calibredb", "ebook-convert", "ebook-meta":
		return calibre.BundleLookPath(name)
	default:
		return exec.LookPath(name)
	}
}

var severityMark = map[config.Severity]string{
	config.SeverityOK:    "OK  ",
	config.SeverityWarn:  "WARN",
	config.SeverityError: "FAIL",
}

// runDoctor prints read-only setup diagnostics and returns an error (non-zero
// exit) only when a check has error severity. lookPath is injectable for
// tests. voiceChecker is nil unless --voice-devices opted in to hardware
// probes (inject a fake in tests).
func runDoctor(cmd *cobra.Command, cfg *config.Config, modelsDir string, lookPath func(string) (string, error), voiceChecker config.VoiceDeviceChecker, asJSON bool) error {
	diags := config.Diagnose(cfg, modelsDir, lookPath)

	if voiceChecker != nil {
		parent := cmd.Context()
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, voiceDeviceProbeTimeout)
		defer cancel()
		diags = append(diags, config.DiagnoseVoiceDevices(ctx, voiceChecker)...)
	}

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
	doctorCmd.Flags().BoolVar(&doctorVoiceDevices, "voice-devices", false, "Also probe microphone/speaker availability (touches audio hardware)")
	rootCmd.AddCommand(doctorCmd)
}
