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
	"github.com/Obedience-Corp/samantha/internal/session"
	"github.com/Obedience-Corp/samantha/internal/stt"
	"github.com/Obedience-Corp/samantha/internal/tts"
	appTUI "github.com/Obedience-Corp/samantha/internal/tui"
	"github.com/Obedience-Corp/samantha/internal/ui"
)

var (
	textMode bool
	noVoice  bool
	skipTUI  bool
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
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// Launch TUI unless --text or --no-voice flags skip it.
		if !skipTUI && !textMode && !noVoice {
			shouldStart, err := appTUI.Run(cfg)
			if err != nil {
				return err
			}
			if !shouldStart {
				return nil // user quit from TUI
			}
			// Reload config in case settings changed.
			cfg, err = config.Load()
			if err != nil {
				return fmt.Errorf("reload config: %w", err)
			}
		}

		return startPipeline(cfg, nil)
	},
}

func init() {
	rootCmd.Flags().BoolVarP(&textMode, "text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolVarP(&noVoice, "no-voice", "n", false, "Disable TTS output")
	rootCmd.Flags().BoolVar(&skipTUI, "no-tui", false, "Skip TUI launcher, start directly")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func startPipeline(cfg *config.Config, resumeSession *session.Session) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	req := config.AssetRequest{
		NeedTTS: !noVoice,
		NeedSTT: !textMode,
		NeedVAD: !textMode && cfg.VADEnabled,
	}
	if err := config.EnsureRuntimeAssets(cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	display := ui.New(bus, cfg.AgentName)

	p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}
	defer cleanup()

	// Create or resume session.
	model := cfg.OllamaModel
	if cfg.BrainProvider == "claude" {
		model = "claude"
	}
	sess := resumeSession
	if sess == nil {
		sess = session.New(cfg.BrainProvider, model)
	} else {
		// Restore conversation history.
		p.Brain.LoadHistory(sess.Turns)
		fmt.Printf("  Resuming session %s (%d turns)\n", sess.ID, len(sess.Turns))
	}

	// Auto-save after each turn.
	p.OnTurn = func() {
		_ = sess.Save(p.Brain.History())
	}

	display.ShowWelcome()
	display.ShowProviders(cfg.TTSProvider, cfg.STTProvider)
	defer display.ShowGoodbye()

	err = app.Run(ctx, p, textMode, noVoice)

	// Final save on exit.
	_ = sess.Save(p.Brain.History())

	return err
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

	// Brain — select provider based on config.
	b, err := brain.NewProvider(cfg)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("init brain: %w", err)
	}
	p.Brain = b

	// TTS + Player (skip in no-voice mode).
	if !silent {
		p.Player = audio.NewPlayer()

		ttsProvider, ttsCleanup, err := tts.NewProvider(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init TTS: %w", err)
		}
		if ttsCleanup != nil {
			cleanups = append(cleanups, ttsCleanup)
		}
		p.TTS = ttsProvider
	}

	// Audio capture + VAD + STT (skip in text mode).
	if !text {
		capture := audio.NewCapture()
		if err := capture.Start(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("start capture: %w", err)
		}
		cleanups = append(cleanups, capture.Stop)
		p.Capture = capture

		var vad *audio.VAD
		if cfg.VADEnabled {
			vad, err = audio.NewVAD(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init VAD: %w", err)
			}
			cleanups = append(cleanups, vad.Delete)
			p.VAD = vad
		}

		sttProvider, sttCleanup, err := stt.NewProvider(cfg, capture, vad)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init STT: %w", err)
		}
		if sttCleanup != nil {
			cleanups = append(cleanups, sttCleanup)
		}
		p.STT = sttProvider
	}

	return p, cleanup, nil
}

var lastProgressPct int

func modelProgress(name string, pct float64) {
	iPct := int(pct)
	if pct == 0 {
		lastProgressPct = -1
		fmt.Printf("  Downloading %s...\n", name)
		return
	}
	if iPct != lastProgressPct {
		lastProgressPct = iPct
		fmt.Printf("\r  %s: %d%%", name, iPct)
		if iPct >= 100 {
			fmt.Println()
		}
	}
}
