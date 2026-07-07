//go:build !integration

package cmd

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/encoder"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"github.com/lancekrogers/samantha/internal/render/extractors"
	"github.com/lancekrogers/samantha/internal/tts"
)

// runRenderText is the real batch render runner: it constructs the TTS provider
// (the only heavy dependency batch rendering needs — no STT, brain, microphone,
// or playback) and renders the input to WAV. EPUB is rendered per chapter into a
// directory; other formats render to a single WAV.
func runRenderText(cmd *cobra.Command, opts render.Options) error {
	// Preflight the optional encoder BEFORE constructing the synthesizer so an
	// unavailable encoder fails fast — no model load, no synthesis, no audio.
	enc, err := render.ResolveEncoder(cmd.Context(), opts, exec.LookPath)
	if err != nil {
		return err
	}

	synth, cleanup, err := newRenderSynth(cmd.Context(), &opts)
	if err != nil {
		return err
	}
	defer cleanup()

	if opts.ResolveFormat() == render.FormatEPUB {
		return runRenderEPUB(cmd, opts, synth, enc)
	}

	text, err := extractRenderText(cmd, &opts)
	if err != nil {
		return err
	}

	result, err := render.RenderText(cmd.Context(), opts, text, synth, audio.WriteWAVFloat32)
	if err != nil {
		return err
	}

	return finishRender(cmd, opts, result.Manifest, enc, renderReport{
		outputKey: "output",
		output:    result.Output,
		wavs:      []string{result.Output},
	}, nil, func(out io.Writer, manifestPath string, encoded []string) {
		fmt.Fprintf(out, "  Rendered %s (%d segment(s), %s)\n", result.Output, result.Segments, result.Duration.Round(10_000_000))
		if manifestPath != "" {
			fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
		}
		for _, e := range encoded {
			fmt.Fprintf(out, "  Encoded:  %s\n", e)
		}
	})
}

// runRenderEPUB renders an EPUB's chapters (in spine order) into per-chapter WAV
// files plus a manifest under --out-dir.
func runRenderEPUB(cmd *cobra.Command, opts render.Options, synth render.Synthesizer, enc encoder.Encoder) error {
	if opts.OutDir == "" {
		return fmt.Errorf("render: EPUB rendering requires --out-dir DIR")
	}

	zr, err := zip.OpenReader(opts.Input)
	if err != nil {
		return fmt.Errorf("render: open %s: %w", opts.Input, err)
	}
	defer zr.Close()

	book, err := epub.Parse(&zr.Reader)
	if err != nil {
		return err
	}
	if opts.Title == "" {
		opts.Title = book.Metadata.Title
	}

	units := make([]render.RenderUnit, 0, len(book.Chapters))
	for _, ch := range book.Chapters {
		data, err := book.ReadChapter(ch.Href)
		if err != nil {
			return err
		}
		doc, err := extractors.ExtractHTML(ch.Href, data)
		if err != nil {
			return err
		}
		title := ch.Title
		if title == "" {
			title = doc.Title
		}
		units = append(units, render.RenderUnit{ID: ch.ID, Title: title, Text: doc.Narration(), SourceRef: ch.Href})
	}

	// RenderUnits records failed chapters and returns the partial manifest
	// even when some chapters fail or the render is cancelled, so the manifest is
	// always persisted and the run stays resumable.
	manifest, renderErr := render.RenderUnits(cmd.Context(), opts, units, synth, audio.WriteWAVFloat32)

	complete, skipped, failed := manifest.Counts()
	return finishRender(cmd, opts, manifest, enc, renderReport{
		outputKey: "output_dir",
		output:    opts.OutDir,
		wavs:      render.CompletedWAVPaths(opts.OutDir, manifest),
	}, renderErr, func(out io.Writer, manifestPath string, encoded []string) {
		fmt.Fprintf(out, "  Rendered %d chapter(s) to %s (%d skipped, %d failed)\n", complete, opts.OutDir, skipped, failed)
		fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
		if len(encoded) > 0 {
			fmt.Fprintf(out, "  Encoded:  %d file(s) to .%s\n", len(encoded), enc.Ext())
		}
	})
}

// newRenderSynth builds the TTS-backed synthesizer for batch rendering, applying
// --voice/--speed and ensuring only TTS assets. It writes the resolved effective
// voice/speed back into opts so manifests and resume keys record what was
// actually used, not just the CLI overrides — a config-driven render is then
// auditable and reproducible from its manifest alone.
func newRenderSynth(ctx context.Context, opts *render.Options) (render.Synthesizer, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	applyVoiceOverrides(cfg, opts)
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return nil, nil, fmt.Errorf("render: TTS assets: %w", err)
	}
	provider, cleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("render: init TTS: %w", err)
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	return &ttsSynth{provider: provider, id: synthIdentityFor(cfg)}, cleanup, nil
}

// applyVoiceOverrides folds CLI --voice/--speed into cfg and writes the
// resolved effective values back into opts, so manifests and resume keys record
// the voice/speed actually used even when they came from config.
func applyVoiceOverrides(cfg *config.Config, opts *render.Options) {
	if opts.Voice != "" {
		cfg.TTSVoice = opts.Voice
	}
	if opts.Speed > 0 {
		cfg.SpeechSpeed = opts.Speed
	}
	opts.Voice = cfg.TTSVoice
	opts.Speed = cfg.SpeechSpeed
}

// synthIdentityFor describes the TTS engine for resume keys: the provider,
// output-affecting provider settings, and the effective voice/speed after config
// plus CLI overrides have been resolved.
func synthIdentityFor(cfg *config.Config) string {
	id := cfg.TTSProvider
	if cfg.TTSVoice != "" {
		id += "/voice=" + cfg.TTSVoice
	}
	if cfg.SpeechSpeed > 0 {
		id += "/speed=" + strconv.FormatFloat(cfg.SpeechSpeed, 'f', -1, 64)
	}
	return id
}

// renderReport names the render's primary output for the shared summary.
type renderReport struct {
	outputKey string // "output" (single file) or "output_dir" (chaptered)
	output    string
	wavs      []string // encode candidates
}

// finishRender is the shared post-render tail for both render paths: persist
// the manifest (stamping CreatedAt outside the deterministic core), run
// optional encoding only on a clean render, and print one summary — so the
// single-file and chaptered --json schemas cannot drift apart. renderErr is
// returned after reporting so scripts get the counts alongside a non-zero exit.
func finishRender(cmd *cobra.Command, opts render.Options, manifest render.RenderManifest, enc encoder.Encoder, rep renderReport, renderErr error, human func(out io.Writer, manifestPath string, encoded []string)) error {
	manifestPath := opts.ManifestPath()
	if manifestPath != "" {
		m := manifest
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := render.WriteManifest(manifestPath, m); err != nil {
			return err
		}
	}

	// Post-process only a clean render: WAVs and the manifest are already on
	// disk, so an encode failure or partial render leaves them inspectable and
	// resumable.
	var encoded []string
	if renderErr == nil && enc != nil {
		out, err := render.EncodeWAVs(cmd.Context(), enc, rep.wavs)
		if err != nil {
			return fmt.Errorf("render: %w", err)
		}
		encoded = out
	}

	out := cmd.OutOrStdout()
	complete, skipped, failed := manifest.Counts()
	if opts.JSON {
		if err := writeRenderJSON(out, map[string]any{
			rep.outputKey: rep.output,
			"manifest":    manifestPath,
			"segments":    len(manifest.Segments),
			"completed":   complete,
			"skipped":     skipped,
			"failed":      failed,
			"encoded":     encoded,
			"sample_rate": manifest.SampleRate,
			"duration_ms": manifest.TotalDurationMS(),
		}); err != nil {
			return err
		}
		return renderErr
	}
	human(out, manifestPath, encoded)
	return renderErr
}

func writeRenderJSON(out io.Writer, payload map[string]any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// extractRenderText reads the input and converts it to narration text according
// to the resolved format. It may set opts.Title from the extracted document.
func extractRenderText(cmd *cobra.Command, opts *render.Options) (string, error) {
	switch f := opts.ResolveFormat(); f {
	case render.FormatText:
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return "", err
		}
		return string(data), nil
	case render.FormatMarkdown:
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return "", err
		}
		doc, err := extractors.ExtractMarkdown(renderSource(*opts), data)
		return narrationFromDoc(opts, doc, err)
	case render.FormatHTML:
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return "", err
		}
		doc, err := extractors.ExtractHTML(renderSource(*opts), data)
		if err != nil {
			return "", err
		}
		return narrationFromDoc(opts, doc, nil)
	case render.FormatURL:
		data, err := extractors.FetchArticle(cmd.Context(), nil, opts.Input, extractors.FetchOptions{})
		if err != nil {
			return "", err
		}
		doc, err := extractors.ExtractHTML(opts.Input, data)
		if err != nil {
			return "", err
		}
		return narrationFromDoc(opts, doc, nil)
	default:
		return "", fmt.Errorf("render: --format %s is not implemented yet", f)
	}
}

// narrationFromDoc returns the document's narration text, adopting its title.
func narrationFromDoc(opts *render.Options, doc render.Document, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if opts.Title == "" {
		opts.Title = doc.Title
	}
	return doc.Narration(), nil
}

// readRenderBytes returns the raw input from stdin or the input file.
func readRenderBytes(cmd *cobra.Command, opts render.Options) ([]byte, error) {
	if opts.Stdin {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("render: read stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(opts.Input)
	if err != nil {
		return nil, fmt.Errorf("render: read %s: %w", opts.Input, err)
	}
	return data, nil
}

func renderSource(opts render.Options) string {
	if opts.Stdin {
		return "stdin"
	}
	return opts.Input
}

// ttsSynth adapts the cgo tts.Provider into the cgo-free render.Synthesizer by
// draining the PCM stream into a sample slice. It carries an id so resume keys
// can invalidate when the underlying TTS engine changes.
type ttsSynth struct {
	provider tts.Provider
	id       string
}

// Identity implements render.SynthIdentity so resume keys fold in the TTS engine.
func (s *ttsSynth) Identity() string { return s.id }

func (s *ttsSynth) Synthesize(ctx context.Context, text string) ([]float32, int, error) {
	stream, err := s.provider.Synthesize(ctx, text)
	if err != nil {
		return nil, 0, err
	}
	rate, err := stream.WaitReady(ctx)
	if err != nil {
		return nil, 0, err
	}
	var samples []float32
	for frame := range stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := stream.Err(); err != nil {
		return nil, 0, err
	}
	return samples, rate, nil
}
