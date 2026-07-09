//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/narrate"
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/render"
)

func runNarratePrepare(cmd *cobra.Command, opts narrate.PrepareOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	bp, err := brain.NewBatchProvider(cfg)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: batch brain unavailable (%v)\n", err)
		fmt.Fprintln(cmd.ErrOrStderr(), "         → sections will be copied unchanged (passthrough); prompts are NOT applied")
		bp = identityBatch{}
	}
	opts.Batch = bp
	opts.ProfileLoader = func(name string) (string, string, string, error) {
		return loadPromptProfile(config.PromptsDir(), name)
	}

	res, err := narrate.Prepare(cmd.Context(), opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"plan":        res.PlanPath,
			"prepared":    res.Prepared,
			"skipped":     res.Skipped,
			"failed":      res.Failed,
			"provider":    res.Provider,
			"model":       res.Model,
			"passthrough": res.Provider == "passthrough",
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Prepared %d section(s) (%d skipped, %d failed)\n", res.Prepared, res.Skipped, res.Failed)
	if res.Provider != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Provider: %s model=%s\n", res.Provider, res.Model)
	}
	return nil
}

// identityBatch copies section text without a model when brain init fails.
type identityBatch struct{}

func (identityBatch) Transform(ctx context.Context, req brain.BatchRequest) (brain.BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return brain.BatchResult{}, err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return brain.BatchResult{}, fmt.Errorf("narrate prepare: empty section text")
	}
	return brain.BatchResult{Text: text, Provider: "passthrough", Model: "identity"}, nil
}

func loadPromptProfile(dir, name string) (system, style, pronunciation string, err error) {
	// Candidates are strictly kind-scoped: a bare <dir>/<name>.md must not
	// satisfy every kind at once, or one file would be duplicated into
	// system, style, AND pronunciation. It remains a system-only fallback.
	candidates := func(kind string) []string {
		return []string{
			filepath.Join(dir, kind, name+".yaml"),
			filepath.Join(dir, kind, name+".yml"),
			filepath.Join(dir, kind, name+".md"),
			filepath.Join(dir, name+"."+kind+".md"),
		}
	}
	firstFile := func(paths ...string) string {
		for _, p := range paths {
			if st, e := os.Stat(p); e == nil && !st.IsDir() {
				return p
			}
		}
		return ""
	}
	find := func(kind string) string { return firstFile(candidates(kind)...) }
	system = find("system")
	if system == "" {
		system = find("persona")
	}
	if system == "" {
		system = firstFile(filepath.Join(dir, name+".md"))
	}
	style = find("style")
	pronunciation = find("pronunciation")
	if system == "" && style == "" && pronunciation == "" {
		// Match catalog names for user documents.
		entries, cerr := prompts.Catalog(dir)
		if cerr == nil {
			for _, e := range entries {
				if strings.EqualFold(e.Name, name) && e.Path != "" {
					system = e.Path
					return system, style, pronunciation, nil
				}
			}
		}
		return "", "", "", fmt.Errorf("narrate prepare: profile %q not found under %s", name, dir)
	}
	return system, style, pronunciation, nil
}

func runNarrateRender(cmd *cobra.Command, opts narrate.RenderOptions) error {
	plan, base, err := narrate.EnsureRenderContext(cmd.Context(), opts.PlanPath)
	if err != nil {
		return err
	}
	units, err := narrate.BuildRenderUnits(plan, base)
	if err != nil {
		return err
	}
	ropts := narrate.RenderPlanOptions(plan, opts, base)
	if ropts.Format == "" || ropts.Format == render.FormatAuto {
		ropts.Format = render.FormatMarkdown
	}
	if err := ropts.Validate(); err != nil {
		return err
	}

	enc, err := render.ResolveEncoder(cmd.Context(), ropts, nil)
	if err != nil {
		return err
	}
	synth, cleanup, err := newRenderSynth(cmd.Context(), &ropts)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(ropts.OutDir, 0o755); err != nil {
		return err
	}
	manifest, renderErr := render.RenderUnits(cmd.Context(), ropts, units, synth, audio.WriteWAVFloat32)
	if err := narrate.RecordRenderOutputs(plan, opts.PlanPath, ropts.OutDir, manifest); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: could not record audio paths in plan: %v\n", err)
	}
	complete, skipped, failed := manifest.Counts()
	return finishRender(cmd, ropts, manifest, enc, renderReport{
		outputKey: "output_dir",
		output:    ropts.OutDir,
		wavs:      render.CompletedWAVPaths(ropts.OutDir, manifest),
	}, renderErr, func(out io.Writer, manifestPath string, encoded []string) {
		fmt.Fprintf(out, "  Rendered %d section(s) to %s (%d skipped, %d failed)\n", complete, ropts.OutDir, skipped, failed)
		fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
		if len(encoded) > 0 && enc != nil {
			fmt.Fprintf(out, "  Encoded:  %d file(s)\n", len(encoded))
		}
	})
}
