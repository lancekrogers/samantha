package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/samantha/internal/app"
	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/brain"
	"github.com/Obedience-Corp/samantha/internal/config"
	"github.com/Obedience-Corp/samantha/internal/events"
	"github.com/Obedience-Corp/samantha/internal/pipeline"
	"github.com/Obedience-Corp/samantha/internal/stt"
	"github.com/Obedience-Corp/samantha/internal/tts"
	"github.com/Obedience-Corp/samantha/internal/ui"
)

var (
	textMode bool
	noVoice  bool
)

var rootCmd = &cobra.Command{
	Use:   "samantha",
	Short: "Give Claude a voice",
	Long:  "Samantha — ultra-low-latency voice assistant for AI coding, inspired by Her.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		_, err := config.Load()
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		if err := config.EnsureModels(cfg, modelProgress); err != nil {
			return fmt.Errorf("ensure models: %w", err)
		}

		bus := events.NewBus()
		display := ui.New(bus)

		p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice)
		if err != nil {
			return fmt.Errorf("init pipeline: %w", err)
		}
		defer cleanup()

		display.ShowWelcome()
		display.ShowProviders(cfg.TTSProvider, cfg.STTProvider)
		defer display.ShowGoodbye()

		return app.Run(ctx, p, textMode, noVoice)
	},
}

func init() {
	rootCmd.Flags().BoolVarP(&textMode, "text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolVarP(&noVoice, "no-voice", "n", false, "Disable TTS output")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func buildPipeline(ctx context.Context, cfg *config.Config, bus *events.Bus, text, silent bool) (*pipeline.Pipeline, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	p := &pipeline.Pipeline{
		Events: bus,
	}

	// Brain is always needed.
	b, err := brain.New(cfg)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("init brain: %w", err)
	}
	p.Brain = b

	// TTS + Player (skip in no-voice mode).
	if !silent {
		p.Player = audio.NewPlayer()

		kokoroTTS, err := tts.NewKokoroTTS(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init TTS: %w", err)
		}
		cleanups = append(cleanups, kokoroTTS.Delete)
		p.TTS = kokoroTTS
	}

	// Audio capture + VAD + STT (skip in text mode).
	if !text {
		capture := audio.NewCapture()
		if err := capture.Start(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("start capture: %w", err)
		}
		cleanups = append(cleanups, capture.Stop)

		vad, err := audio.NewVAD(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init VAD: %w", err)
		}
		cleanups = append(cleanups, vad.Delete)

		sttProvider, err := stt.NewSherpaSTT(cfg, capture, vad)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init STT: %w", err)
		}
		cleanups = append(cleanups, sttProvider.Delete)
		p.STT = sttProvider
	}

	return p, cleanup, nil
}

func modelProgress(name string, pct float64) {
	if pct == 0 {
		fmt.Printf("  Downloading %s...\n", name)
	} else if int(pct)%25 == 0 {
		fmt.Printf("\r  %s: %.0f%%", name, pct)
	}
	if pct >= 100 {
		fmt.Println()
	}
}
