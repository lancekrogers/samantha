package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

var (
	modelsStatusJSON  bool
	modelsStatusScope scopeFlags
	modelsEnsureScope scopeFlags
)

// scopeFlags narrows a models command to specific asset kinds. Flags combine as
// a union; no scope flags (or --all) keeps the full default request.
type scopeFlags struct {
	tts bool
	stt bool
	vad bool
	all bool
}

func (s *scopeFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&s.tts, "tts", false, "Limit to TTS assets")
	cmd.Flags().BoolVar(&s.stt, "stt", false, "Limit to the configured STT provider's assets")
	cmd.Flags().BoolVar(&s.vad, "vad", false, "Limit to the VAD asset")
	cmd.Flags().BoolVar(&s.all, "all", false, "All asset kinds (same as no scope flags)")
}

// request resolves the flags to the asset request for cfg.
func (s scopeFlags) request(cfg *config.Config) config.AssetRequest {
	if s.all || (!s.tts && !s.stt && !s.vad) {
		return config.DefaultAssetRequest(cfg)
	}
	return config.ScopedAssetRequest(cfg, config.AssetScope{STT: s.stt, TTS: s.tts, VAD: s.vad})
}

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Inspect and manage local model assets",
}

var modelsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show required and installed model assets (read-only, offline)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runModelsStatus(cmd, cfg, config.ModelsDir(), modelsStatusScope.request(cfg), modelsStatusJSON)
	},
}

// runModelsStatus resolves the asset manifest for cfg and req and reports each
// asset's installed/missing state under modelsDir. It is read-only and never
// downloads.
func runModelsStatus(cmd *cobra.Command, cfg *config.Config, modelsDir string, req config.AssetRequest, asJSON bool) error {
	manifest, err := config.ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	statuses := manifest.Status(modelsDir)

	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(statuses)
	}

	fmt.Fprintf(out, "\n  Model assets (models dir: %s)\n\n", modelsDir)
	if len(statuses) == 0 {
		fmt.Fprintln(out, "  No model assets required for the current configuration.")
		fmt.Fprintln(out)
		return nil
	}

	missing := 0
	for _, s := range statuses {
		state := "installed"
		if !s.Installed {
			state = "missing — run 'samantha models ensure'"
			missing++
		}
		mode := ""
		if s.Mode != "" {
			mode = "/" + s.Mode
		}
		fmt.Fprintf(out, "  [%s] %s (%s%s) — %s\n", s.Kind, s.Name, s.Provider, mode, state)
	}
	fmt.Fprintf(out, "\n  %d asset(s), %d missing.\n\n", len(statuses), missing)
	return nil
}

var modelsEnsureCmd = &cobra.Command{
	Use:   "ensure",
	Short: "Download any missing model assets for the current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runModelsEnsure(cmd, cfg, modelsEnsureScope.request(cfg), config.EnsureRuntimeAssets)
	},
}

// ensureAssetsFunc matches config.EnsureRuntimeAssets so tests can observe the
// request without downloading.
type ensureAssetsFunc func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(name string, pct float64)) error

// runModelsEnsure downloads the missing assets in req for cfg, reporting each
// asset as it begins and a final status line. It returns an actionable error
// naming the failing asset if a download fails.
func runModelsEnsure(cmd *cobra.Command, cfg *config.Config, req config.AssetRequest, ensure ensureAssetsFunc) error {
	out := cmd.OutOrStdout()

	started := map[string]bool{}
	err := ensure(cmd.Context(), cfg, req, func(name string, pct float64) {
		if !started[name] {
			started[name] = true
			fmt.Fprintf(out, "  downloading %s ...\n", name)
		}
	})
	if err != nil {
		return fmt.Errorf("models ensure: %w", err)
	}

	if len(started) == 0 {
		fmt.Fprintln(out, "  All required model assets are already present.")
	} else {
		fmt.Fprintf(out, "  Done — %d asset(s) ensured.\n", len(started))
	}
	return nil
}

func init() {
	modelsStatusCmd.Flags().BoolVar(&modelsStatusJSON, "json", false, "Output machine-readable JSON")
	modelsStatusScope.register(modelsStatusCmd)
	modelsEnsureScope.register(modelsEnsureCmd)
	modelsCmd.AddCommand(modelsStatusCmd)
	modelsCmd.AddCommand(modelsEnsureCmd)
	rootCmd.AddCommand(modelsCmd)
}
