package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/narrate"
	"github.com/lancekrogers/samantha/internal/render"
)

// newNarrateCmd builds the `samantha narrate` command group.
func newNarrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "narrate",
		Short: "Plan, prepare, and render prompt-controlled narration",
	}
	cmd.AddCommand(newNarratePlanCmd())
	cmd.AddCommand(newNarratePrepareCmd())
	cmd.AddCommand(newNarrateRenderCmd())
	return cmd
}

func newNarratePlanCmd() *cobra.Command {
	var (
		out       string
		overwrite bool
		asJSON    bool
		format    string
	)
	cmd := &cobra.Command{
		Use:   "plan INPUT --out PLAN.yaml",
		Short: "Create a narration plan from Markdown, HTML, EPUB, or PDF",
		Long: `Extract sections from a document into a samantha.narration-plan.v1 YAML
plan plus extracted/<id>.txt files.

Supported inputs: Markdown, HTML, EPUB, and digital PDF (via pdftotext).
Existing plans are not overwritten unless --overwrite is set.

Examples:
  samantha narrate plan article.md --out narration.plan.yaml
  samantha narrate plan book.epub --out out/book.plan.yaml --json
  samantha narrate plan book.pdf --out out/book.plan.yaml`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := narrate.PlanOptions{
				Input:     args[0],
				Out:       out,
				Overwrite: overwrite,
				Format:    render.Format(format),
			}
			res, err := narrate.BuildPlan(cmd.Context(), opts, narrate.PDFExtractor{})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"plan":     res.PlanPath,
					"sections": res.SectionCount,
					"format":   res.Format,
					"warnings": res.Warnings,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Narration plan: %s\n", res.PlanPath)
			fmt.Fprintf(cmd.OutOrStdout(), "  Format: %s  sections: %d\n", res.Format, res.SectionCount)
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "  Warning: %s\n", w)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Write the narration plan YAML to PATH (required)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Replace an existing plan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a machine-readable summary")
	cmd.Flags().StringVar(&format, "format", "auto", "Input format: auto|markdown|html|epub|pdf")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newNarratePrepareCmd() *cobra.Command {
	var (
		systemPrompt, stylePrompt, pronunciation, profile string
		outDir                                            string
		resume, asJSON, overwrite                         bool
	)
	cmd := &cobra.Command{
		Use:   "prepare PLAN.yaml",
		Short: "Transform planned sections with prompt-controlled batch brain",
		Long: `Prepare each extracted section into narration-ready Markdown using a
history-free batch brain transform.

Prompt files may be plain Markdown or samantha.prompt.v1 YAML. --profile NAME
loads named prompts from the user prompts directory; explicit --system-prompt /
--style-prompt / --pronunciation flags override profile entries.

Examples:
  samantha narrate prepare narration.plan.yaml --system-prompt prompts/system.md --resume
  samantha narrate prepare narration.plan.yaml --profile audiobook --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNarratePrepare(cmd, narrate.PrepareOptions{
				PlanPath:      args[0],
				SystemPrompt:  systemPrompt,
				StylePrompt:   stylePrompt,
				Pronunciation: pronunciation,
				Profile:       profile,
				OutDir:        outDir,
				Resume:        resume,
				Overwrite:     overwrite,
				JSON:          asJSON,
			})
		},
	}
	cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt file (Markdown or prompt YAML)")
	cmd.Flags().StringVar(&stylePrompt, "style-prompt", "", "Style prompt file")
	cmd.Flags().StringVar(&pronunciation, "pronunciation", "", "Pronunciation prompt file")
	cmd.Flags().StringVar(&profile, "profile", "", "Named prompt profile from the user prompts directory")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "Write prepared sections under DIR (default: <plan-dir>/prepared)")
	cmd.Flags().BoolVar(&resume, "resume", false, "Skip sections with unchanged hashes")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Rebuild all sections")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a machine-readable summary")
	return cmd
}

func newNarrateRenderCmd() *cobra.Command {
	var (
		resume, asJSON bool
		audioFormat    string
		voice          string
		speed          float64
	)
	cmd := &cobra.Command{
		Use:   "render PLAN.yaml",
		Short: "Render prepared narration sections to audio",
		Long: `Render prepared sections from a narration plan through the batch render
runtime (one WAV per section + manifest). Run 'samantha narrate prepare' first.

Examples:
  samantha narrate render narration.plan.yaml --resume
  samantha narrate render narration.plan.yaml --audio-format mp3 --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNarrateRender(cmd, narrate.RenderOptions{
				PlanPath:    args[0],
				Resume:      resume,
				JSON:        asJSON,
				AudioFormat: audioFormat,
				Voice:       voice,
				Speed:       speed,
			})
		},
	}
	cmd.Flags().BoolVar(&resume, "resume", false, "Skip completed sections with matching text hash")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a machine-readable summary")
	cmd.Flags().StringVar(&audioFormat, "audio-format", "", "Also encode to mp3|m4a|m4b|aac|opus")
	cmd.Flags().StringVar(&voice, "voice", "", "Override plan/config TTS voice")
	cmd.Flags().Float64Var(&speed, "speed", 0, "Override plan/config speech speed")
	return cmd
}

// planBaseDir returns the directory containing the plan file.
func planBaseDir(planPath string) string {
	return filepath.Dir(planPath)
}
