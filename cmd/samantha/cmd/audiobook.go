package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audiobook"
	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
)

// configLoader supplies the loaded samantha config. Commands take it injected
// so tests can substitute a fixed config, mirroring the renderRunner pattern.
type configLoader func() (*config.Config, error)

// newAudiobookCmd builds the `samantha audiobook` command group. Audiobook
// subcommands are task-oriented wrappers over the render runtime, not a second
// renderer: they validate in audiobook vocabulary and map onto render.Options.
func newAudiobookCmd(run renderRunner, loadConfig configLoader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audiobook",
		Short: "Create audiobooks from books",
	}
	cmd.AddCommand(newAudiobookCreateCmd(run, loadConfig))
	cmd.AddCommand(newAudiobookPreviewCmd(loadConfig))
	cmd.AddCommand(newAudiobookPlanCmd())
	cmd.AddCommand(newAudiobookReviewCmd())
	return cmd
}

// newAudiobookCreateCmd builds `samantha audiobook create`. It shares render's
// runner and pass-through flags so the two commands cannot drift apart.
func newAudiobookCreateCmd(run renderRunner, loadConfig configLoader) *cobra.Command {
	var (
		opts        render.Options
		fromLibrary string
	)

	cmd := &cobra.Command{
		Use:   "create [INPUT] --out-dir DIR",
		Short: "Create an audiobook from an EPUB or PDF (one file per chapter/page, resumable)",
		Long: `Create an audiobook from an EPUB or digital PDF: one WAV per chapter (EPUB
spine) or page (PDF) plus a manifest under --out-dir, using the same batch
render runtime as 'samantha render'.

Use --from-library QUERY to resolve INPUT from the Calibre library (requires
calibre_enabled=true). Mutually exclusive with a positional input path.

Use 'samantha render' for markdown, html, url, and text sources. For
prompt-controlled PDF cleanup, prefer 'samantha narrate plan|prepare|render'.

Examples:
  samantha audiobook create book.epub --out-dir out/book
  samantha audiobook create book.pdf --out-dir out/book
  samantha audiobook create book.epub --out-dir out/book --audio-format m4b
  samantha audiobook create book.epub --out-dir out/book --resume --json
  samantha audiobook create --from-library "Crypto 101" --out-dir out/crypto`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Input = args[0]
			}
			if err := resolveFromLibraryFlag(cmd, loadConfig, &opts, fromLibrary, len(args) > 0); err != nil {
				return err
			}
			if err := validateAudiobookInput("create", opts); err != nil {
				return err
			}
			// Preserve auto-detected format (epub or pdf).
			if opts.Format == "" || opts.Format == render.FormatAuto {
				opts.Format = opts.ResolveFormat()
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return run(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.OutDir, "out-dir", "", "Write chapter files and a manifest to DIR (required)")
	cmd.Flags().StringVar(&fromLibrary, "from-library", "", "Resolve INPUT from Calibre library search query (mutually exclusive with positional INPUT)")
	addRenderPassthroughFlags(cmd, &opts)

	return cmd
}

// resolveFromLibraryFlag substitutes opts.Input from a Calibre query when
// fromLibrary is set. positionalSet is true when the user also passed INPUT.
func resolveFromLibraryFlag(cmd *cobra.Command, loadConfig configLoader, opts *render.Options, fromLibrary string, positionalSet bool) error {
	fromLibrary = strings.TrimSpace(fromLibrary)
	if fromLibrary == "" {
		return nil
	}
	if positionalSet || strings.TrimSpace(opts.Input) != "" {
		return fmt.Errorf("--from-library is mutually exclusive with a positional input path")
	}
	if loadConfig == nil {
		return fmt.Errorf("--from-library: config loader unavailable")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if !cfg.CalibreEnabled {
		return fmt.Errorf("--from-library: calibre is disabled; enable with: samantha config calibre_enabled true")
	}
	client := calibreClientFromConfig(cfg)
	path, format, err := resolveLibraryBook(cmd.Context(), client, fromLibrary)
	if err != nil {
		return fmt.Errorf("--from-library: %w", err)
	}
	opts.Input = path
	if opts.Format == "" || opts.Format == render.FormatAuto {
		opts.Format = format
	}
	return nil
}

// resolveLibraryBook resolves a Calibre query to an audiobook-ready EPUB/PDF
// path, converting a MOBI/AZW-family source when necessary.
func resolveLibraryBook(ctx context.Context, client calibre.Client, query string) (path string, format render.Format, err error) {
	book, err := client.Resolve(ctx, query)
	if err != nil {
		return "", "", err
	}
	p, fmtName, err := client.BestFormatPathContext(ctx, book)
	if err != nil {
		return "", "", err
	}
	return p, render.Format(fmtName), nil
}

// newAudiobookPreviewCmd builds `samantha audiobook preview`. Preview is
// read-only: it validates like create, resolves config plus flag overrides,
// and reports what a render would produce — no models loaded, no files written.
func newAudiobookPreviewCmd(loadConfig configLoader) *cobra.Command {
	var opts render.Options

	cmd := &cobra.Command{
		Use:   "preview INPUT --out-dir DIR",
		Short: "Preview an audiobook render without producing audio",
		Long: `Preview what 'samantha audiobook create' would do: the detected format, output
layout, effective voice/speed after config and flag resolution, and the
equivalent 'samantha render' command line. Nothing is rendered.

Examples:
  samantha audiobook preview book.epub --out-dir out/book
  samantha audiobook preview book.epub --out-dir out/book --audio-format m4b --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Input = args[0]
			}
			if err := validateAudiobookInput("preview", opts); err != nil {
				return err
			}
			if opts.Format == "" || opts.Format == render.FormatAuto {
				opts.Format = opts.ResolveFormat()
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			applyVoiceOverrides(cfg, &opts)
			return writeAudiobookPreview(cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.OutDir, "out-dir", "", "Write chapter files and a manifest to DIR (required)")
	addRenderPassthroughFlags(cmd, &opts)

	return cmd
}

// validateAudiobookInput checks an audiobook invocation in audiobook
// vocabulary before handing off to render's own validation. verb names the
// subcommand for error messages.
func validateAudiobookInput(verb string, opts render.Options) error {
	if strings.TrimSpace(opts.Input) == "" {
		return fmt.Errorf("audiobook %s: provide an EPUB or PDF input path", verb)
	}
	switch opts.ResolveFormat() {
	case render.FormatEPUB, render.FormatPDF:
	default:
		return fmt.Errorf("audiobook %s: only EPUB or PDF input is supported; use samantha render for markdown, html, url, and text sources", verb)
	}
	if opts.OutDir == "" {
		return fmt.Errorf("audiobook %s: provide --out-dir DIR for chapter output", verb)
	}
	return nil
}

// writeAudiobookPreview reports the resolved preview, human-readable or as
// stable JSON fields with --json.
func writeAudiobookPreview(out io.Writer, opts render.Options) error {
	command := renderCommandLine(opts)
	if opts.JSON {
		return writeRenderJSON(out, map[string]any{
			"input":          opts.Input,
			"format":         string(opts.ResolveFormat()),
			"output_dir":     opts.OutDir,
			"manifest":       opts.ManifestPath(),
			"voice":          opts.Voice,
			"speed":          opts.Speed,
			"resume":         opts.Resume,
			"audio_format":   opts.AudioFormat,
			"encoder":        previewEncoder(opts),
			"render_command": command,
		})
	}

	fmt.Fprintln(out, "  Audiobook preview")
	fmt.Fprintf(out, "    input:    %s\n", opts.Input)
	fmt.Fprintf(out, "    format:   %s\n", opts.ResolveFormat())
	fmt.Fprintf(out, "    out dir:  %s\n", opts.OutDir)
	fmt.Fprintf(out, "    manifest: %s\n", opts.ManifestPath())
	if opts.Voice != "" {
		fmt.Fprintf(out, "    voice:    %s\n", opts.Voice)
	}
	if opts.Speed > 0 {
		fmt.Fprintf(out, "    speed:    %s\n", formatSpeed(opts.Speed))
	}
	fmt.Fprintf(out, "    resume:   %t\n", opts.Resume)
	if opts.AudioFormat != "" {
		fmt.Fprintf(out, "    encode:   %s (%s)\n", opts.AudioFormat, previewEncoder(opts))
	}
	fmt.Fprintf(out, "    command:  %s\n", command)
	fmt.Fprintln(out, "  (preview only — nothing was rendered)")
	return nil
}

// renderCommandLine returns the equivalent `samantha render` invocation for
// the resolved options, shell-quoted so paths with spaces copy-paste correctly.
func renderCommandLine(opts render.Options) string {
	parts := []string{"samantha", "render", shellQuote(opts.Input), "--out-dir", shellQuote(opts.OutDir)}
	if opts.Voice != "" {
		parts = append(parts, "--voice", shellQuote(opts.Voice))
	}
	if opts.Speed > 0 {
		parts = append(parts, "--speed", formatSpeed(opts.Speed))
	}
	if opts.Manifest != "" {
		parts = append(parts, "--manifest", shellQuote(opts.Manifest))
	}
	if opts.Resume {
		parts = append(parts, "--resume")
	}
	if opts.Overwrite {
		parts = append(parts, "--overwrite")
	}
	if opts.AudioFormat != "" {
		parts = append(parts, "--audio-format", opts.AudioFormat)
	}
	if opts.EncoderBin != "" {
		parts = append(parts, "--encoder", shellQuote(opts.EncoderBin))
	}
	if opts.JSON {
		parts = append(parts, "--json")
	}
	return strings.Join(parts, " ")
}

// previewEncoder names the encoder binary a render would use, or "" when no
// encoded output was requested.
func previewEncoder(opts render.Options) string {
	if opts.AudioFormat == "" {
		return ""
	}
	if opts.EncoderBin != "" {
		return opts.EncoderBin
	}
	return "ffmpeg"
}

// shellQuote single-quotes s when it contains characters the shell would
// interpret, so previewed commands copy-paste correctly.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'`$&|;<>()*?[]#~!\\{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func formatSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

// newAudiobookPlanCmd builds the read-only extraction and classification step.
func newAudiobookPlanCmd() *cobra.Command {
	var (
		outDir, format    string
		overwrite, asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "plan INPUT --out-dir DIR",
		Short: "Build a reviewable audiobook production plan without TTS",
		Long: `Extract an EPUB or PDF into a production-plan.yaml plus extracted text
and a human-readable production-plan.md preview. No audio is rendered.

Examples:
  samantha audiobook plan book.epub --out-dir out/book
  samantha audiobook plan book.epub --out-dir out/book --overwrite --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := audiobook.BuildPlan(cmd.Context(), audiobook.PlanOptions{
				Input: args[0], OutDir: outDir, Format: render.Format(format), Overwrite: overwrite,
			})
			if err != nil {
				return err
			}
			unresolved := len(res.Plan.Unresolved())
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"plan": res.PlanPath, "preview": res.MDPath, "sections": len(res.Plan.Sections), "unresolved": unresolved,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Audiobook production plan: %s\n", res.PlanPath)
			fmt.Fprintf(cmd.OutOrStdout(), "  Preview: %s\n", res.MDPath)
			fmt.Fprintf(cmd.OutOrStdout(), "  Sections: %d  unresolved: %d\n", len(res.Plan.Sections), unresolved)
			fmt.Fprintln(cmd.OutOrStdout(), "  No audio was rendered.")
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "", "Write the production plan and extracted text to DIR (required)")
	cmd.Flags().StringVar(&format, "format", "auto", "Input format: auto|epub|pdf")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Replace existing production-plan.yaml")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a machine-readable summary")
	_ = cmd.MarkFlagRequired("out-dir")
	return cmd
}

// newAudiobookReviewCmd prints a plan and optionally applies explicit human
// decisions. It is intentionally file-based so YAML/Markdown remain inspectable
// and the TUI can be added later without creating a second source of truth.
func newAudiobookReviewCmd() *cobra.Command {
	var includes, excludes []string
	var reason string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "review PLAN.yaml",
		Short: "Review or update audiobook production-plan decisions",
		Long: `Show every planned section and its include/exclude/review decision.
Use --include and --exclude to apply explicit decisions, then inspect the
updated production-plan.md before rendering.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := audiobook.Load(args[0])
			if err != nil {
				return err
			}
			changed := len(includes) > 0 || len(excludes) > 0
			if changed {
				if err := plan.ApplyDecisions(includes, excludes, reason); err != nil {
					return err
				}
				if err := plan.Save(args[0]); err != nil {
					return err
				}
				if err := plan.WriteMarkdown(strings.TrimSuffix(args[0], filepath.Ext(args[0])) + ".md"); err != nil {
					return err
				}
			}
			return writeAudiobookReview(cmd, args[0], plan, changed, asJSON)
		},
	}
	cmd.Flags().StringSliceVar(&includes, "include", nil, "Mark section IDs for narration (repeat or comma-separate)")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Mark section IDs to omit (repeat or comma-separate)")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason recorded for explicit decisions")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a machine-readable summary")
	return cmd
}

func writeAudiobookReview(cmd *cobra.Command, path string, plan *audiobook.Plan, changed, asJSON bool) error {
	unresolved := plan.Unresolved()
	if asJSON {
		rows := make([]map[string]any, 0, len(plan.Sections))
		for _, sec := range plan.Sections {
			rows = append(rows, map[string]any{"id": sec.ID, "order": sec.Order, "title": sec.Title, "kind": sec.Kind, "suggestion": sec.Suggestion, "decision": sec.Decision, "estimated_duration_ms": sec.EstimatedDurationMS})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"plan": path, "changed": changed, "sections": rows, "unresolved": len(unresolved)})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Audiobook review: %s\n", path)
	for _, sec := range plan.Sections {
		fmt.Fprintf(cmd.OutOrStdout(), "  %02d %-8s %-14s %s\n", sec.Order, sec.Decision, sec.Kind, sec.Title)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  unresolved: %d\n", len(unresolved))
	if changed {
		fmt.Fprintln(cmd.OutOrStdout(), "  decisions saved")
	}
	return nil
}
