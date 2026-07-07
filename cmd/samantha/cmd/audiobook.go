package cmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

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
	cmd.AddCommand(newAudiobookCreateCmd(run))
	cmd.AddCommand(newAudiobookPreviewCmd(loadConfig))
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
			if err := validateAudiobookInput("create", opts); err != nil {
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
			opts.Format = render.FormatEPUB
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
		return fmt.Errorf("audiobook %s: provide an EPUB input path", verb)
	}
	if opts.ResolveFormat() != render.FormatEPUB {
		return fmt.Errorf("audiobook %s: only EPUB input is supported yet; use samantha render for markdown, html, url, and text sources", verb)
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
