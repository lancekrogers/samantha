package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

var (
	modelsCleanUnused bool
	modelsCleanDryRun bool
	modelsCleanYes    bool
	modelsCleanJSON   bool
	modelsCleanPlan   string
)

// cleanOptions are the flags one `models clean` invocation was given.
type cleanOptions struct {
	Unused bool
	DryRun bool
	Yes    bool
	JSON   bool
	// Plan is the dry-run document (or bare plan id) the caller was shown:
	// a file path, or "-" for stdin. Empty means none was supplied.
	Plan string
}

// stdoutIsTerminalFn reports whether a human is watching stdout; tests swap it
// to exercise both sides of the interactive gate.
var stdoutIsTerminalFn = stdoutIsTerminal

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
	Short: "Remove model assets nothing in this install references",
	Long: `Remove model assets nothing in this install references.

The required set is the union of the global config, every persona profile, and
every config key that names an asset — including ones the active mode does not
load, such as sherpa_streaming_model while stt_mode is offline. If that set
cannot be resolved, clean exits non-zero and removes nothing.

--dry-run lists what would be removed and, under "Kept", every asset that is
being preserved and why. --yes removes that list. When stdout is not a
terminal, --yes also requires --plan: the --dry-run --json document (or its
plan_id), so an apply can only ever delete a list its caller has seen. If the
candidate set changed in between, clean reports plan_changed and removes
nothing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// LoadRaw, not Load: Load overlays the ACTIVE persona onto the
		// returned config, which would hide the config file's own provider
		// (a kokoro install whose active persona speaks qwen3-tts would stop
		// protecting the kokoro pack). Every persona, active or not, is
		// walked separately below. It also keeps this preview command free of
		// Load's migration writes.
		cfg, err := config.LoadRaw()
		if err != nil {
			return err
		}
		return runModelsClean(cmd, cfg, config.ModelsDir(), cleanOptions{
			Unused: modelsCleanUnused,
			DryRun: modelsCleanDryRun,
			Yes:    modelsCleanYes,
			JSON:   modelsCleanJSON,
			Plan:   modelsCleanPlan,
		})
	},
}

// runModelsClean lists the paths under modelsDir that nothing the install
// references claims, and deletes them when --yes is set.
//
// An apply only ever deletes the list the caller was shown: --plan pins that
// list by id, and a non-interactive caller must supply one.
func runModelsClean(cmd *cobra.Command, cfg *config.Config, modelsDir string, opts cleanOptions) error {
	if !opts.Unused {
		return fmt.Errorf("models clean: --unused is required (only unused-asset cleanup is supported)")
	}
	if opts.DryRun == opts.Yes {
		return fmt.Errorf("models clean: choose exactly one of --dry-run or --yes")
	}
	requested, err := readCleanPlan(cmd, opts.Plan)
	if err != nil {
		return err
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
	if opts.DryRun {
		if opts.JSON {
			return writeJSON(cmd, plan)
		}
		printCleanPlan(out, plan, true)
		return nil
	}
	if err := gateCleanApply(cmd, opts, requested, plan); err != nil {
		return err
	}
	if !opts.JSON {
		printCleanPlan(out, plan, false)
	}

	result, err := config.DeleteCleanPlan(cmd.Context(), modelsDir, plannedCandidates(requested, plan), plan.Candidates)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeJSON(cmd, result)
	}
	printCleanResult(out, result)
	return nil
}

// gateCleanApply refuses any apply that is not pinned to a list the caller
// actually saw: a stale plan deletes nothing, and a non-interactive caller
// without a plan is refused outright. An interactive caller has just been
// shown the list on the terminal.
func gateCleanApply(cmd *cobra.Command, opts cleanOptions, requested, current config.CleanPlan) error {
	if opts.Plan == "" {
		if !stdoutIsTerminalFn() {
			return &ExitCodeError{Code: exitUsage, Err: errors.New("clean: --plan is required when not interactive")}
		}
		return nil
	}
	if requested.PlanID == current.PlanID {
		return nil
	}
	changed := config.NewPlanChangedError(requested.PlanID, current.PlanID)
	if opts.JSON {
		if err := writeJSON(cmd, changed); err != nil {
			return err
		}
	}
	return &ExitCodeError{Code: exitOperationFailed, Err: changed}
}

// plannedCandidates are the paths the apply may touch: the caller's list when
// it carried one, otherwise the list just printed. The ids already match, so
// these agree; iterating the caller's copy keeps the promise that only what
// was shown gets deleted.
func plannedCandidates(requested, current config.CleanPlan) []config.CleanCandidate {
	if len(requested.Candidates) > 0 {
		return requested.Candidates
	}
	return current.Candidates
}

// readCleanPlan loads --plan from a file, or from stdin when it is "-".
func readCleanPlan(cmd *cobra.Command, value string) (config.CleanPlan, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return config.CleanPlan{}, nil
	}
	var (
		data []byte
		err  error
	)
	if value == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(value)
	}
	if err != nil {
		return config.CleanPlan{}, fmt.Errorf("clean: reading plan: %w", err)
	}
	plan, err := config.ParseCleanPlan(data)
	if err != nil {
		return config.CleanPlan{}, &ExitCodeError{Code: exitUsage, Err: err}
	}
	return plan, nil
}

// printCleanResult reports what was removed and what was left alone.
func printCleanResult(out io.Writer, result config.CleanApplyResult) {
	fmt.Fprintf(out, "  Deleted %d candidate(s), %s freed.\n", len(result.Deleted), formatBytes(result.BytesFreed))
	for _, s := range result.Skipped {
		fmt.Fprintf(out, "  Skipped %s — %s\n", s.Path, s.Reason)
	}
	fmt.Fprintln(out)
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
	modelsCleanCmd.Flags().StringVar(&modelsCleanPlan, "plan", "",
		"Apply exactly the reviewed dry-run plan: a --dry-run --json file, \"-\" for stdin, or its plan_id. Required with --yes when stdout is not a terminal")
	modelsCmd.AddCommand(modelsCleanCmd)
}
