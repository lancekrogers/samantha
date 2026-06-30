//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/tts"
)

// runRenderText is the real batch render runner: it reads text/stdin input,
// constructs the TTS provider (the only heavy dependency batch rendering needs —
// no STT, brain, microphone, or playback), and writes a WAV file. Document
// formats other than plain text are added in later tasks.
func runRenderText(cmd *cobra.Command, opts render.Options) error {
	if f := opts.ResolveFormat(); f != render.FormatText {
		return fmt.Errorf("render: --format %s is not implemented yet", f)
	}

	text, err := readRenderInput(cmd, opts)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if opts.Voice != "" {
		cfg.TTSVoice = opts.Voice
	}
	if opts.Speed > 0 {
		cfg.SpeechSpeed = opts.Speed
	}
	if err := config.EnsureRuntimeAssets(cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return fmt.Errorf("render: TTS assets: %w", err)
	}

	provider, cleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return fmt.Errorf("render: init TTS: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	result, err := render.RenderText(cmd.Context(), opts, text, &ttsSynth{provider: provider}, audio.WriteWAVFloat32)
	if err != nil {
		return err
	}

	// Write the manifest when one is requested (always for multi-file).
	manifestPath := opts.ManifestPath()
	if manifestPath != "" {
		m := result.Manifest
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := render.WriteManifest(manifestPath, m); err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	complete, skipped, failed := result.Manifest.Counts()
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"output":      result.Output,
			"manifest":    manifestPath,
			"segments":    result.Segments,
			"completed":   complete,
			"skipped":     skipped,
			"failed":      failed,
			"sample_rate": result.SampleRate,
			"duration_ms": result.Duration.Milliseconds(),
		})
	}
	fmt.Fprintf(out, "  Rendered %s (%d segment(s), %s)\n", result.Output, result.Segments, result.Duration.Round(10_000_000))
	if manifestPath != "" {
		fmt.Fprintf(out, "  Manifest: %s\n", manifestPath)
	}
	return nil
}

// readRenderInput returns the input text from stdin or the input file.
func readRenderInput(cmd *cobra.Command, opts render.Options) (string, error) {
	if opts.Stdin {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("render: read stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(opts.Input)
	if err != nil {
		return "", fmt.Errorf("render: read %s: %w", opts.Input, err)
	}
	return string(data), nil
}

// ttsSynth adapts the cgo tts.Provider into the cgo-free render.Synthesizer by
// draining the PCM stream into a sample slice.
type ttsSynth struct{ provider tts.Provider }

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
