package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/render"
)

// renderRunner executes a validated render invocation. The cgo command layer
// supplies the real synthesizing runner; the integration build supplies a
// plan-only runner. This keeps the command definition (flags + validation)
// cgo-free and shared across both binaries.
type renderRunner func(cmd *cobra.Command, opts render.Options) error

// newRenderCmd builds the `samantha render` command. It parses and validates
// flags (cgo-free) and delegates execution to run.
func newRenderCmd(run renderRunner) *cobra.Command {
	var opts render.Options

	cmd := &cobra.Command{
		Use:   "render [input]",
		Short: "Render text, articles, or books to audio (batch, scriptable)",
		Long: `Render documents to audio files without the live voice pipeline.

Batch narration is noninteractive and scriptable: it reads text, Markdown, HTML,
URL articles, or EPUB and writes WAV files plus a manifest.

Examples:
  samantha render article.md --out out/article.wav
  cat notes.txt | samantha render --stdin --out notes.wav
  samantha render book.epub --out-dir out/book --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Input = args[0]
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return run(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&opts.Stdin, "stdin", false, "Read input text from stdin")
	f.StringVar((*string)(&opts.Format), "format", string(render.FormatAuto), "Input format: text|markdown|html|url|epub|auto")
	f.StringVar(&opts.Out, "out", "", "Write a single audio file to PATH")
	f.StringVar(&opts.OutDir, "out-dir", "", "Write chapter/segment files and a manifest to DIR")
	f.StringVar(&opts.Voice, "voice", "", "Override the configured TTS voice")
	f.Float64Var(&opts.Speed, "speed", 0, "Override the configured speech speed")
	f.StringVar(&opts.Title, "title", "", "Override the document title")
	f.BoolVar(&opts.JSON, "json", false, "Print a machine-readable summary")
	f.BoolVar(&opts.Resume, "resume", false, "Skip completed manifest entries with matching text hash")
	f.BoolVar(&opts.Overwrite, "overwrite", false, "Replace existing outputs")

	return cmd
}

// runRenderPlan reports the resolved render plan. The synthesis runtime is wired
// in the next task; for now this validates the invocation and shows what would
// be produced.
func runRenderPlan(cmd *cobra.Command, opts render.Options) error {
	out := cmd.OutOrStdout()

	input := "stdin"
	if !opts.Stdin {
		input = opts.Input
	}
	output := opts.Out
	if opts.MultiFile() {
		output = opts.OutDir + " (multi-file + manifest)"
	}

	fmt.Fprintln(out, "  Render plan")
	fmt.Fprintf(out, "    input:  %s\n", input)
	fmt.Fprintf(out, "    format: %s\n", opts.ResolveFormat())
	fmt.Fprintf(out, "    output: %s\n", output)
	if opts.Voice != "" {
		fmt.Fprintf(out, "    voice:  %s\n", opts.Voice)
	}
	fmt.Fprintln(out, "  (render runtime is wired in the next task; no audio produced yet)")
	return nil
}
