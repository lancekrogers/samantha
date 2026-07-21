//go:build !integration

package cmd

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/narrate"
	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/encoder"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"github.com/lancekrogers/samantha/internal/render/extractors"
	"github.com/lancekrogers/samantha/internal/tts"
)

// runRenderText is the real batch render runner: it constructs the TTS provider
// (the only heavy dependency batch rendering needs — no STT, brain, microphone,
// or playback) and renders the input to WAV. EPUB is rendered per chapter into a
// directory; Markdown/HTML/URL use sectioned multi-file output when --out-dir is
// set, otherwise a single WAV; plain text is always single-file.
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

	switch opts.ResolveFormat() {
	case render.FormatEPUB:
		return runRenderEPUB(cmd, opts, synth, enc)
	case render.FormatPDF:
		return runRenderPDF(cmd, opts, synth, enc)
	}

	if opts.MultiFile() {
		return runRenderStructured(cmd, opts, synth, enc)
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

// runRenderPDF extracts a digital PDF via pdftotext and renders pages as units
// (--out-dir) or concatenated narration (--out). Low text density prints a
// warning pointing at samantha narrate plan.
func runRenderPDF(cmd *cobra.Command, opts render.Options, synth render.Synthesizer, enc encoder.Encoder) error {
	pages, warnings, err := (narrate.PDFExtractor{}).ExtractPages(cmd.Context(), opts.Input)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: %s\n", w)
		fmt.Fprintf(cmd.ErrOrStderr(), "         → use 'samantha narrate plan' for prompt-controlled cleanup\n")
	}
	if opts.Title == "" {
		opts.Title = filepath.Base(opts.Input)
	}
	if opts.MultiFile() {
		units := make([]render.RenderUnit, 0, len(pages))
		for _, p := range pages {
			units = append(units, render.RenderUnit{
				ID:        fmt.Sprintf("page-%03d", p.Page),
				Title:     fmt.Sprintf("Page %d", p.Page),
				Text:      p.Text,
				SourceRef: opts.Input,
			})
		}
		manifest, renderErr := render.RenderUnits(cmd.Context(), opts, units, synth, audio.WriteWAVFloat32)
		complete, skipped, failed := manifest.Counts()
		return finishRender(cmd, opts, manifest, enc, renderReport{
			outputKey: "output_dir",
			output:    opts.OutDir,
			wavs:      render.CompletedWAVPaths(opts.OutDir, manifest),
		}, renderErr, func(out io.Writer, manifestPath string, encoded []string) {
			fmt.Fprintf(out, "  Rendered %d page(s) to %s (%d skipped, %d failed)\n", complete, opts.OutDir, skipped, failed)
			fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
			if len(encoded) > 0 {
				fmt.Fprintf(out, "  Encoded:  %d file(s) to .%s\n", len(encoded), enc.Ext())
			}
		})
	}
	// Single-file: join pages.
	var b strings.Builder
	for i, p := range pages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(p.Text)
	}
	result, err := render.RenderText(cmd.Context(), opts, b.String(), synth, audio.WriteWAVFloat32)
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

// runRenderStructured renders Markdown/HTML/URL inputs as sectioned multi-file
// output under --out-dir via Document.Units and RenderUnits.
func runRenderStructured(cmd *cobra.Command, opts render.Options, synth render.Synthesizer, enc encoder.Encoder) error {
	if opts.OutDir == "" {
		return fmt.Errorf("render: sectioned rendering requires --out-dir DIR")
	}

	doc, err := extractRenderDocument(cmd, &opts)
	if err != nil {
		return err
	}
	if opts.Title == "" {
		opts.Title = doc.Title
	}

	units := doc.Units()
	manifest, renderErr := render.RenderUnits(cmd.Context(), opts, units, synth, audio.WriteWAVFloat32)

	complete, skipped, failed := manifest.Counts()
	return finishRender(cmd, opts, manifest, enc, renderReport{
		outputKey: "output_dir",
		output:    opts.OutDir,
		wavs:      render.CompletedWAVPaths(opts.OutDir, manifest),
	}, renderErr, func(out io.Writer, manifestPath string, encoded []string) {
		fmt.Fprintf(out, "  Rendered %d section(s) to %s (%d skipped, %d failed)\n", complete, opts.OutDir, skipped, failed)
		fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
		if len(encoded) > 0 {
			fmt.Fprintf(out, "  Encoded:  %d file(s) to .%s\n", len(encoded), enc.Ext())
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
	cliVoice, cliSpeed := opts.Voice, opts.Speed
	applyVoiceOverrides(cfg, opts)
	if strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "qwen3-tts") {
		if cliVoice != "" {
			return nil, nil, errors.New("render: qwen3-tts does not support --voice; use the model's default voice")
		}
		if cliSpeed > 0 {
			return nil, nil, errors.New("render: qwen3-tts does not support --speed")
		}
		// The shared config defaults are Kokoro-specific. Qwen uses the
		// model-native voice and speed, so keep those unused values out of the
		// manifest and resume key rather than claiming they affected audio.
		cfg.TTSVoice = ""
		cfg.SpeechSpeed = 0
		opts.Voice = ""
		opts.Speed = 0
	}
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
	return &ttsSynth{
		provider:            provider,
		id:                  synthIdentityFor(cfg),
		voice:               cfg.TTSVoice,
		speed:               cfg.SpeechSpeed,
		mode:                tts.VoiceMode(cfg.QwenTTSMode),
		qwenVoice:           cfg.QwenTTSVoice,
		language:            cfg.QwenTTSLanguage,
		instruction:         cfg.QwenTTSInstruction,
		referenceAudio:      cfg.QwenTTSReferenceAudio,
		referenceTranscript: cfg.QwenTTSReferenceText,
	}, cleanup, nil
}

// synthIdentityFor describes the TTS engine for resume keys: the provider,
// output-affecting provider settings, and the effective voice/speed after config
// plus CLI overrides have been resolved.
func synthIdentityFor(cfg *config.Config) string {
	id := strings.TrimSpace(cfg.TTSProvider)
	if strings.EqualFold(id, "qwen3-tts") {
		id = "qwen3-tts"
		if model := strings.TrimSpace(cfg.QwenTTSModel); model != "" {
			id += "/model=" + model
		}
		if binary := strings.TrimSpace(cfg.QwenTTSBinary); binary != "" {
			id += "/binary=" + binary
		}
		if mode := strings.TrimSpace(cfg.QwenTTSMode); mode != "" {
			id += "/mode=" + mode
		}
		if voice := strings.TrimSpace(cfg.QwenTTSVoice); voice != "" {
			id += "/voice=" + voice
		}
		if language := strings.TrimSpace(cfg.QwenTTSLanguage); language != "" {
			id += "/language=" + language
		}
		if instruction := strings.TrimSpace(cfg.QwenTTSInstruction); instruction != "" {
			id += "/instruction-sha256=" + stringIdentityHash(instruction)
		}
		if referenceAudio := strings.TrimSpace(cfg.QwenTTSReferenceAudio); referenceAudio != "" {
			id += "/reference-audio-sha256=" + fileIdentityHash(referenceAudio)
		}
		if referenceText := strings.TrimSpace(cfg.QwenTTSReferenceText); referenceText != "" {
			id += "/reference-text-sha256=" + stringIdentityHash(referenceText)
		}
		return id
	}
	if cfg.TTSVoice != "" {
		id += "/voice=" + cfg.TTSVoice
	}
	if cfg.SpeechSpeed > 0 {
		id += "/speed=" + strconv.FormatFloat(cfg.SpeechSpeed, 'f', -1, 64)
	}
	return id
}

func stringIdentityHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func fileIdentityHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unreadable:" + path
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable:" + path
	}
	return fmt.Sprintf("%x", h.Sum(nil))
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

// extractRenderText reads the input and converts it to narration text according
// to the resolved format. It may set opts.Title from the extracted document.
func extractRenderText(cmd *cobra.Command, opts *render.Options) (string, error) {
	if opts.ResolveFormat() == render.FormatText {
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	doc, err := extractRenderDocument(cmd, opts)
	if err != nil {
		return "", err
	}
	if opts.Title == "" {
		opts.Title = doc.Title
	}
	return doc.Narration(), nil
}

// extractRenderDocument extracts a structure-aware Document for Markdown, HTML,
// or URL inputs. Plain text and EPUB are not handled here.
func extractRenderDocument(cmd *cobra.Command, opts *render.Options) (render.Document, error) {
	switch f := opts.ResolveFormat(); f {
	case render.FormatMarkdown:
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return render.Document{}, err
		}
		return extractors.ExtractMarkdownPolicy(renderSource(*opts), data, opts.EffectiveCodeBlocks())
	case render.FormatHTML:
		data, err := readRenderBytes(cmd, *opts)
		if err != nil {
			return render.Document{}, err
		}
		return extractors.ExtractHTML(renderSource(*opts), data)
	case render.FormatURL:
		data, err := extractors.FetchArticle(cmd.Context(), nil, opts.Input, extractors.FetchOptions{})
		if err != nil {
			return render.Document{}, err
		}
		return extractors.ExtractHTML(opts.Input, data)
	default:
		return render.Document{}, fmt.Errorf("render: --format %s does not support document extraction", f)
	}
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
// can invalidate when the underlying TTS engine changes. When the provider
// implements tts.RequestProvider, typed batch-render requests carry metadata.
type ttsSynth struct {
	provider            tts.Provider
	id                  string
	voice               string
	speed               float64
	mode                tts.VoiceMode
	qwenVoice           string
	language            string
	instruction         string
	referenceAudio      string
	referenceTranscript string
}

// Identity implements render.SynthIdentity so resume keys fold in the TTS engine.
func (s *ttsSynth) Identity() string { return s.id }

func (s *ttsSynth) Synthesize(ctx context.Context, text string) ([]float32, int, error) {
	return s.SynthesizeRequest(ctx, render.SynthesisRequest{Text: text, Voice: s.voice, Speed: s.speed})
}

// SynthesizeRequest implements render.RequestSynthesizer.
func (s *ttsSynth) SynthesizeRequest(ctx context.Context, req render.SynthesisRequest) ([]float32, int, error) {
	if req.Voice == "" {
		req.Voice = s.voice
	}
	if req.Speed == 0 {
		req.Speed = s.speed
	}
	if rp, ok := s.provider.(tts.RequestProvider); ok {
		result, err := rp.SynthesizeRequest(ctx, tts.SynthesisRequest{
			Text:                req.Text,
			Voice:               firstNonEmpty(s.qwenVoice, req.Voice),
			Mode:                s.mode,
			Language:            s.language,
			Instruction:         s.instruction,
			ReferenceAudio:      s.referenceAudio,
			ReferenceTranscript: s.referenceTranscript,
			Speed:               req.Speed,
			Metadata:            req.Metadata,
		})
		if err != nil {
			return nil, 0, err
		}
		return drainPCMStream(ctx, result.Stream, result.SampleRate)
	}
	stream, err := s.provider.Synthesize(ctx, req.Text)
	if err != nil {
		return nil, 0, err
	}
	return drainPCMStream(ctx, stream, 0)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func drainPCMStream(ctx context.Context, stream *audio.PCMStream, knownRate int) ([]float32, int, error) {
	rate := knownRate
	if rate == 0 {
		var err error
		rate, err = stream.WaitReady(ctx)
		if err != nil {
			return nil, 0, err
		}
	} else if _, err := stream.WaitReady(ctx); err != nil {
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
