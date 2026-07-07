package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/render"
)

// newAudiobookCmd builds the `samantha audiobook` command group. Audiobook
// subcommands are task-oriented wrappers over the render runtime, not a second
// renderer: they validate in audiobook vocabulary and map onto render.Options.
func newAudiobookCmd(run renderRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audiobook",
		Short: "Create audiobooks from books",
	}
	cmd.AddCommand(newAudiobookCreateCmd(run))
	return cmd
}

// newAudiobookCreateCmd builds `samantha audiobook create`. It shares render's
// runner and pass-through flags so the two commands cannot drift apart.
func newAudiobookCreateCmd(run renderRunner) *cobra.Command {
	var opts render.Options

	cmd := &cobra.Command{
		Use:   "create INPUT --out-dir DIR",
		Short: "Create an audiobook from an EPUB (one file per chapter, resumable)",
		Long: `Create an audiobook from an EPUB: one WAV per chapter (spine order) plus a
manifest under --out-dir, using the same batch render runtime as
'samantha render'.

Only EPUB input is supported yet; use 'samantha render' for markdown, html,
url, and text sources.

Examples:
  samantha audiobook create book.epub --out-dir out/book
  samantha audiobook create book.epub --out-dir out/book --audio-format m4b
  samantha audiobook create book.epub --out-dir out/book --resume --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Input = args[0]
			}
			if err := validateAudiobookCreate(opts); err != nil {
				return err
			}
			opts.Format = render.FormatEPUB
			if err := opts.Validate(); err != nil {
				return err
			}
			return run(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.OutDir, "out-dir", "", "Write chapter files and a manifest to DIR (required)")
	addRenderPassthroughFlags(cmd, &opts)

	return cmd
}

// validateAudiobookCreate checks the invocation in audiobook vocabulary before
// handing off to render's own validation.
func validateAudiobookCreate(opts render.Options) error {
	if strings.TrimSpace(opts.Input) == "" {
		return fmt.Errorf("audiobook create: provide an EPUB input path")
	}
	if opts.ResolveFormat() != render.FormatEPUB {
		return fmt.Errorf("audiobook create: only EPUB input is supported yet; use samantha render for markdown, html, url, and text sources")
	}
	if opts.OutDir == "" {
		return fmt.Errorf("audiobook create: provide --out-dir DIR for chapter output")
	}
	return nil
}
