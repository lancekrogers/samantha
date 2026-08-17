package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

var (
	modelsCleanUnused bool
	modelsCleanDryRun bool
	modelsCleanYes    bool
	modelsCleanJSON   bool
)

// personaProfilesFn lists persona profiles; tests swap it so no test ever
// reads the real install root.
var personaProfilesFn = persona.List

// requiredAssets resolves everything the install references — the global
// config, every persona, and every config-referenced asset — as the set clean
// must never touch.
//
// It fails closed: a persona that cannot be listed or resolved aborts the
// clean rather than shrinking the required set. Before this existed, "required"
// meant the global manifest alone, so personas pinned to a provider the global
// config did not select had their models classified as unused.
func requiredAssets(ctx context.Context, cfg *config.Config, modelsDir string) (config.RequiredSet, error) {
	personas, err := cleanPersonaSources(cfg)
	if err != nil {
		return config.RequiredSet{}, err
	}
	return config.RequiredAssetPaths(ctx, cfg, modelsDir, personas)
}

// cleanPersonaSources derives each persona's effective config through
// persona.Apply, the same overlay the running agent uses, so the assets a
// persona speaks through are exactly the ones protected.
//
// Each persona gets its own copy of cfg. Apply only writes scalar fields, so
// the shallow copy never mutates the caller's config.
func cleanPersonaSources(cfg *config.Config) ([]config.PersonaAssets, error) {
	profiles, err := personaProfilesFn()
	if err != nil {
		return nil, err
	}
	sources := make([]config.PersonaAssets, 0, len(profiles))
	for _, profile := range profiles {
		if profile == nil {
			return nil, fmt.Errorf("persona profile could not be resolved")
		}
		derived := *cfg
		persona.Apply(&derived, profile)
		sources = append(sources, config.PersonaAssets{ID: profile.ID, Cfg: &derived})
	}
	return sources, nil
}

var modelsCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clean model assets not required by the current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runModelsClean(cmd, cfg, config.ModelsDir(), modelsCleanUnused, modelsCleanDryRun, modelsCleanYes, modelsCleanJSON)
	},
}

// runModelsClean lists the paths under modelsDir that the currently required
// manifest (the full default request for cfg) does not claim, or deletes them
// when --yes is explicitly set.
func runModelsClean(cmd *cobra.Command, cfg *config.Config, modelsDir string, unused, dryRun, yes, asJSON bool) error {
	if !unused {
		return fmt.Errorf("models clean: --unused is required (only unused-asset cleanup is supported)")
	}
	if dryRun == yes {
		return fmt.Errorf("models clean: choose exactly one of --dry-run or --yes")
	}

	required, err := requiredAssets(cmd.Context(), cfg, modelsDir)
	if err != nil {
		return fmt.Errorf("clean: cannot determine required assets: %w", err)
	}
	plan, err := required.CleanPlan(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if asJSON && dryRun {
		return encodeCleanJSON(out, plan)
	}
	if !asJSON {
		printCleanPlan(out, plan, dryRun)
	}
	if dryRun {
		return nil
	}

	result, err := config.DeleteCleanCandidates(cmd.Context(), modelsDir, plan.Candidates)
	if err != nil {
		return err
	}
	if asJSON {
		return encodeCleanJSON(out, result)
	}
	fmt.Fprintf(out, "  Deleted %d candidate(s), %s total.\n\n", len(result.Deleted), formatBytes(result.Bytes))
	return nil
}

// encodeCleanJSON writes one indented JSON document.
func encodeCleanJSON(out io.Writer, payload any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// printCleanPlan prints exactly what a deletion would touch and exactly what
// it would keep, with reasons. Both lists are always shown: the 2026-08-17
// data loss began with a prompt that named a count and a size but no paths.
func printCleanPlan(out io.Writer, plan config.CleanPlan, dryRun bool) {
	mode := "dry run"
	if !dryRun {
		mode = "apply"
	}
	fmt.Fprintf(out, "\n  Unused model assets (models dir: %s) — %s\n\n", plan.ModelsDir, mode)
	if len(plan.Candidates) == 0 {
		fmt.Fprintln(out, "  No removable assets.")
	}
	for _, c := range plan.Candidates {
		fmt.Fprintf(out, "  %s (%s) [%s]\n", c.Path, formatBytes(c.Size), c.Category)
	}
	if len(plan.Candidates) > 0 && dryRun {
		fmt.Fprintf(out, "\n  %d candidate(s), %s total. Nothing was deleted.\n",
			len(plan.Candidates), formatBytes(plan.TotalBytes))
	}
	printCleanKept(out, plan.Protected)
	fmt.Fprintln(out)
}

// printCleanKept lists every protected path with the config key or persona
// that keeps it.
func printCleanKept(out io.Writer, protected []config.ProtectedPath) {
	if len(protected) == 0 {
		return
	}
	fmt.Fprintf(out, "\n  Kept (%d) — referenced by your configuration and personas:\n", len(protected))
	for _, p := range protected {
		fmt.Fprintf(out, "    %s — %s\n", p.Path, p.Reason)
	}
}

// formatBytes renders a byte count with a binary unit suffix.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func init() {
	modelsCleanCmd.Flags().BoolVar(&modelsCleanUnused, "unused", false, "Select assets not required by the current configuration")
	modelsCleanCmd.Flags().BoolVar(&modelsCleanDryRun, "dry-run", false, "Preview removable assets without deleting anything")
	modelsCleanCmd.Flags().BoolVar(&modelsCleanYes, "yes", false, "Delete unused model assets without prompting")
	modelsCleanCmd.Flags().BoolVar(&modelsCleanJSON, "json", false, "Output machine-readable JSON")
	modelsCmd.AddCommand(modelsCleanCmd)
}
