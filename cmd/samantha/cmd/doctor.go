package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

var (
	doctorJSON         bool
	doctorVoiceDevices bool
)

// voiceDeviceProbeTimeout bounds the opt-in hardware probe so a wedged audio
// backend cannot hang doctor.
const voiceDeviceProbeTimeout = 3 * time.Second

// newVoiceDeviceChecker is set by doctor_voice.go (!integration) to the real
// audio.DeviceChecker. The integration Linux binary leaves this nil so it can
// build with CGO_ENABLED=0 (no sherpa).
var newVoiceDeviceChecker func() config.VoiceDeviceChecker

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
			if newVoiceDeviceChecker == nil {
				return fmt.Errorf("doctor --voice-devices is not available in this build")
			}
			checker = newVoiceDeviceChecker()
		}
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
	diags = append(diags, personaQwenDiags(cfg, modelsDir)...)

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

// personaQwenDiags reports personas whose speech cannot start: they route
// through qwen3-tts but the installed native package lacks their tier (or is
// absent entirely). The active provider alone cannot see this — a kokoro
// session with a qwen persona still needs the package.
func personaQwenDiags(cfg *config.Config, modelsDir string) []config.Diagnostic {
	profiles, err := persona.List()
	if err != nil {
		return nil
	}
	var diags []config.Diagnostic
	for _, p := range profiles {
		if p == nil || !strings.EqualFold(strings.TrimSpace(p.TTS.Provider), managedqwen.ProviderName) {
			continue
		}
		tier := strings.TrimSpace(p.TTS.Tier)
		if tier == "" && cfg != nil {
			tier = strings.TrimSpace(cfg.QwenTTSModelTier)
		}
		tier = managedqwen.NormalizeModelTier(tier)
		st := managedqwen.InspectNative(modelsDir, tier)
		if st.Installed && st.ModelReady {
			continue
		}
		detail := fmt.Sprintf("persona %q speaks through qwen3-tts tier %s, which is not installed", p.ID, tier)
		if st.Installed {
			detail = fmt.Sprintf("persona %q pins qwen3-tts tier %s but the installed package lacks it", p.ID, tier)
		}
		diags = append(diags, config.Diagnostic{
			Name:        "qwen persona " + p.ID,
			Severity:    config.SeverityWarn,
			Detail:      detail,
			Remediation: "run samantha models ensure --tts (or Settings → Qwen) to install the package for this tier",
		})
	}
	return diags
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Output machine-readable JSON")
	doctorCmd.Flags().BoolVar(&doctorVoiceDevices, "voice-devices", false, "Also probe microphone/speaker availability (touches audio hardware)")
	rootCmd.AddCommand(doctorCmd)
}
