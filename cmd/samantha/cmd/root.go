//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/app"
	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
	"github.com/lancekrogers/samantha/internal/ui"
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
		if _, err := config.Load(); err != nil {
			return err
		}
		// Best-effort: seed default prompt documents so users have editable
		// starting files. A failure here (e.g. a read-only home) must not
		// block commands — resolution falls back to the embedded defaults.
		if _, err := prompts.Seed(config.PromptsDir()); err != nil {
			fmt.Fprintf(os.Stderr, "samantha: seeding prompt defaults: %v\n", err)
		}
		return nil
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
	return fang.Execute(context.Background(), rootCmd)
}

func startPipeline(cfg *config.Config, resumeSession *session.Session) error {
	ctx, cancel := signalContext()
	defer cancel()

	req := config.AssetRequest{
		NeedTTS: !noVoice,
		NeedSTT: !textMode,
		NeedVAD: !textMode && cfg.VADEnabled,
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	display := ui.New(bus, cfg.AgentName)

	p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}
	defer cleanup()

	// Preload the model while the welcome/setup is shown so the user's first
	// turn isn't the cold one. Best-effort — failures are ignored.
	if w, ok := p.Brain.(brain.Warmer); ok {
		go w.Warmup(ctx)
	}

	// Create or resume session.
	var model string
	switch cfg.BrainProvider {
	case "claude":
		model = "claude"
	case "grok":
		model = cfg.GrokModel
		if model == "" {
			model = "grok"
		}
	default:
		model = cfg.OllamaModel
	}
	sess := resumeSession
	if sess == nil {
		sess = session.New(cfg.BrainProvider, model)
	} else {
		// Restore conversation history.
		if sess.Provider != "" && sess.Provider != cfg.BrainProvider {
			fmt.Printf("  Note: session was recorded with provider %q, resuming with %q\n", sess.Provider, cfg.BrainProvider)
		}
		p.Brain.LoadHistory(sess.Turns)
		fmt.Printf("  Resuming session %s (%d turns)\n", sess.ID, len(sess.Turns))
	}

	// Auto-save after each turn.
	p.OnTurn = func() {
		if err := sess.Save(p.Brain.History()); err != nil {
			bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
		}
	}

	display.ShowWelcome()
	display.ShowProviders(cfg.TTSProvider, cfg.STTProvider)
	defer display.ShowGoodbye()

	err = app.Run(ctx, p, os.Stdin, textMode, noVoice)

	// Final save on exit.
	if saveErr := sess.Save(p.Brain.History()); saveErr != nil {
		fmt.Fprintf(os.Stderr, "  warning: failed to save session %s: %v\n", sess.ID, saveErr)
	}

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
		Events:            bus,
		VoiceToolsEnabled: cfg.VoiceToolsEnabled,
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
		player := audio.NewPlayer()
		cleanups = append(cleanups, func() { _ = player.Close() })
		p.Player = player

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
		var frontend *audio.VoiceFrontend
		if cfg.VoiceFrontendEnabled {
			frontend = audio.NewVoiceFrontend()
			cleanups = append(cleanups, func() { _ = frontend.Close() })
		}

		capture := audio.NewCapture()
		if frontend != nil {
			capture.SetFrontend(frontend)
		}
		if err := capture.Start(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("start capture: %w", err)
		}
		cleanups = append(cleanups, capture.Stop)
		p.Capture = capture

		if !silent && frontend != nil {
			if player, ok := p.Player.(*audio.Player); ok {
				player.SetFrontend(frontend)
			}
		}

		var vad *audio.VAD
		if cfg.VADEnabled {
			vad, err = audio.NewVAD(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init VAD: %w", err)
			}
			cleanups = append(cleanups, vad.Delete)
			p.VAD = vad

			// Barge-in stays nil (watchBargeIn no-ops) unless explicitly enabled.
			if !silent && cfg.BargeInEnabled {
				bargeInVAD, err := audio.NewBargeInVAD(cfg)
				if err != nil {
					cleanup()
					return nil, nil, fmt.Errorf("init barge-in VAD: %w", err)
				}
				cleanups = append(cleanups, bargeInVAD.Delete)
				p.BargeInVAD = bargeInVAD
			}
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
