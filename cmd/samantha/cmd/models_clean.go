package cmd

import (
	"bufio"
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

// stdoutIsTerminalFn and stdinIsTerminalFn report whether a human is at the
// other end; tests swap them to exercise both sides of the interactive gate.
var (
	stdoutIsTerminalFn = stdoutIsTerminal
	stdinIsTerminalFn  = stdinIsTerminal
)

// personaProfilesFn lists persona profiles; tests swap it so no test ever
// reads the real install root.
var personaProfilesFn = persona.List

// personaDirFn locates the personas directory; paired with personaProfilesFn
// so a test can describe a whole install.
var personaDirFn = persona.Dir

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
	if err := allPersonasResolved(profiles); err != nil {
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

// allPersonasResolved refuses when the personas directory holds more profiles
// than were loaded. persona.List skips a directory whose name is not a valid
// id rather than failing, and a persona clean cannot see is a persona whose
// models clean would offer to delete.
func allPersonasResolved(profiles []*persona.Profile) error {
	dir := personaDirFn()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading personas dir %s: %w", dir, err)
	}
	onDisk := 0
	for _, e := range entries {
		if e.IsDir() {
			onDisk++
		}
	}
	if onDisk > len(profiles) {
		return fmt.Errorf("%d of %d persona directories under %s could not be loaded (a directory name must be lowercase kebab-case)",
			onDisk-len(profiles), onDisk, dir)
	}
	return nil
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
being preserved and why. --yes removes that list, and only ever a list its
caller has seen: at a terminal it prints the list and asks for confirmation,
and anywhere else it requires --plan, the --dry-run --json document. If the
candidate set or the models dir changed in between, clean reports plan_changed
and removes nothing.`,
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
		return runModelsClean(cmd, cfg, config.ModelsDirFrom(cfg), cleanOptions{
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
// An apply only ever deletes a list its caller has seen: --plan pins that list
// by id and by models dir, and an apply without one has to be confirmed at a
// terminal after the list is printed.
func runModelsClean(cmd *cobra.Command, cfg *config.Config, modelsDir string, opts cleanOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	requested, err := readCleanPlan(cmd, opts.Plan)
	if err != nil {
		return failClean(cmd, opts, "plan_invalid", err)
	}

	required, err := requiredAssets(cmd.Context(), cfg, modelsDir)
	if err != nil {
		return failClean(cmd, opts, "required_assets", fmt.Errorf("clean: cannot determine required assets: %w", err))
	}
	plan, err := required.CleanPlan(cmd.Context())
	if err != nil {
		return failClean(cmd, opts, "required_assets", err)
	}

	out := cmd.OutOrStdout()
	if opts.DryRun {
		if opts.JSON {
			return writeJSON(cmd, plan)
		}
		printCleanPlan(out, plan, true)
		return nil
	}
	if !opts.JSON {
		// Print before the gate: a caller without a plan is about to be asked
		// to confirm this exact list.
		printCleanPlan(out, plan, false)
	}
	if err := gateCleanApply(cmd, opts, requested, plan); err != nil {
		return err
	}
	return applyCleanPlan(cmd, opts, modelsDir, requested, plan)
}

// validate rejects flag combinations that would leave what gets deleted, or
// who reviewed it, ambiguous.
func (opts cleanOptions) validate() error {
	if !opts.Unused {
		return fmt.Errorf("models clean: --unused is required (only unused-asset cleanup is supported)")
	}
	if opts.DryRun == opts.Yes {
		return fmt.Errorf("models clean: choose exactly one of --dry-run or --yes")
	}
	if opts.DryRun && opts.Plan != "" {
		// A dry run produces a plan; it never consumes one. Accepting the flag
		// here would let a caller believe a list had been checked against
		// something.
		return fmt.Errorf("models clean: --plan applies to --yes, not --dry-run")
	}
	if opts.Yes && opts.JSON && opts.Plan == "" {
		// --json is a program, and a program cannot be the human who read the
		// list. There is no terminal to confirm at.
		return &ExitCodeError{Code: exitUsage, Err: errors.New("clean: --plan is required with --yes --json")}
	}
	return nil
}

// applyCleanPlan removes the reviewed list and reports what happened. A
// failure part-way through still reports what was already deleted: the caller
// needs to know what left the disk.
func applyCleanPlan(cmd *cobra.Command, opts cleanOptions, modelsDir string, requested, plan config.CleanPlan) error {
	result, err := config.DeleteCleanPlan(cmd.Context(), modelsDir, plannedCandidates(requested, plan), plan.Candidates)
	out := cmd.OutOrStdout()
	if err != nil {
		if opts.JSON {
			if encodeErr := writeJSON(cmd, cleanFailure{Error: "delete_failed", Message: err.Error(), Result: &result}); encodeErr != nil {
				return encodeErr
			}
			return &ExitCodeError{Code: exitOperationFailed, Err: err}
		}
		printCleanResult(out, result)
		return err
	}
	if opts.JSON {
		return writeJSON(cmd, result)
	}
	printCleanResult(out, result)
	return nil
}

// cleanFailure is the machine-readable failure shape. A --json caller must
// never be left with an empty stdout and a banner on stderr it cannot parse.
type cleanFailure struct {
	Error   string                   `json:"error"`
	Message string                   `json:"message"`
	Result  *config.CleanApplyResult `json:"result,omitempty"`
}

// failClean reports err as JSON when the caller asked for it, and always exits
// non-zero.
func failClean(cmd *cobra.Command, opts cleanOptions, kind string, err error) error {
	if !opts.JSON {
		return err
	}
	if encodeErr := writeJSON(cmd, cleanFailure{Error: kind, Message: err.Error()}); encodeErr != nil {
		return encodeErr
	}
	return &ExitCodeError{Code: exitOperationFailed, Err: err}
}

// gateCleanApply refuses any apply that is not pinned to a list the caller
// actually saw: a stale plan deletes nothing, a plan captured against another
// install is not this install's plan, and an apply without a plan must be
// confirmed by a human at a terminal — isatty alone is not consent, so the
// answer is read.
func gateCleanApply(cmd *cobra.Command, opts cleanOptions, requested, current config.CleanPlan) error {
	if opts.Plan == "" {
		return confirmCleanApply(cmd, current)
	}
	if requested.PlanID == current.PlanID && requested.ModelsDir == current.ModelsDir {
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

// confirmCleanApply asks the human who just read the printed list to say yes.
func confirmCleanApply(cmd *cobra.Command, plan config.CleanPlan) error {
	if !stdoutIsTerminalFn() || !stdinIsTerminalFn() {
		return &ExitCodeError{Code: exitUsage, Err: errors.New("clean: --plan is required when not interactive")}
	}
	if len(plan.Candidates) == 0 {
		return nil
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  Delete %d item(s), %s? [y/N] ", len(plan.Candidates), formatBytes(plan.TotalBytes))
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		return &ExitCodeError{Code: exitUsage, Err: fmt.Errorf("clean: reading confirmation: %w", err)}
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		fmt.Fprintln(out, "  Nothing was deleted.")
		return &ExitCodeError{Code: exitOperationFailed, Err: errors.New("clean: not confirmed")}
	}
}

// plannedCandidates are the paths the apply may touch: the caller's list when
// it carried one, otherwise the list just printed and confirmed.
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
	if len(plan.Candidates) > 0 {
		fmt.Fprintf(out, "\n  %d candidate(s), %s total.", len(plan.Candidates), formatBytes(plan.TotalBytes))
		if dryRun {
			fmt.Fprint(out, " Nothing was deleted.")
		}
		fmt.Fprintln(out)
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
		"Apply exactly the reviewed dry-run plan: the --dry-run --json document, as a file or \"-\" for stdin. Required with --yes unless a human confirms at a terminal")
	modelsCmd.AddCommand(modelsCleanCmd)
}
