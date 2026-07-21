//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/fang"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/app"
	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
	"github.com/lancekrogers/samantha/internal/ui"
)

var (
	textMode      bool
	noVoice       bool
	skipTUI       bool
	debugAudioDir string
)

var rootCmd = &cobra.Command{
	Use:   "samantha",
	Short: "Give Claude a voice",
	Long:  "Samantha — ultra-low-latency voice assistant for AI coding, inspired by Her.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.Load(); err != nil {
			return err
		}
		debugDir := debugAudioDir
		if debugDir == "auto" {
			debugDir = filepath.Join(config.ConfigDir(), "debug", "audio")
		}
		if err := audio.SetDebugAudioDir(debugDir); err != nil {
			return err
		}
		if debugDir != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Audio debug capture enabled under %s\n", debugDir)
		}
		// Best-effort: seed default prompt documents so users have editable
		// starting files. A failure here (e.g. a read-only home) must not
		// block commands — resolution falls back to the embedded defaults.
		if shouldSeedPrompts(cmd) {
			if _, err := prompts.Seed(config.PromptsDir()); err != nil {
				fmt.Fprintf(os.Stderr, "samantha: seeding prompt defaults: %v\n", err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// The TUI serves interactive invocations including --text (voice
		// input off, D3). Non-TTY, --no-tui, and --no-voice without --text
		// keep the plain stdout loop (scripts / demos without mic).
		if useConversationTUI() {
			return appTUI.RunWithMeeting(cfg, conversationRuntimeBuilder(nil), meetingRuntimeBuilder())
		}

		return startPipeline(cfg, nil)
	},
}

// useConversationTUI is true for a terminal session that should open the
// Bubble Tea UI. --text enters the TUI even with --no-voice so demos and
// typing-only use still get the conversation screen (and tool visibility).
func useConversationTUI() bool {
	if skipTUI || !stdinIsTerminal() {
		return false
	}
	// Plain --no-voice (no --text): headless stdout loop for scripts.
	if noVoice && !textMode {
		return false
	}
	return true
}

func shouldSeedPrompts(cmd *cobra.Command) bool {
	return cmd.CommandPath() != "samantha config migrate"
}

func init() {
	rootCmd.Flags().BoolVarP(&textMode, "text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolVarP(&noVoice, "no-voice", "n", false, "Disable TTS output")
	rootCmd.Flags().BoolVar(&skipTUI, "no-tui", false, "Skip TUI launcher, start directly")
	rootCmd.PersistentFlags().StringVar(&debugAudioDir, "debug-audio", "", "Record TTS source WAVs, exact device-output WAV, and callback timing (optional DIR)")
	rootCmd.PersistentFlags().Lookup("debug-audio").NoOptDefVal = "auto"
}

// Execute runs the root command.
func Execute() error {
	return fang.Execute(context.Background(), rootCmd)
}

func stdinIsTerminal() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func modelName(cfg *config.Config) string {
	switch cfg.BrainProvider {
	case "claude":
		return "claude"
	case "grok":
		if cfg.GrokModel != "" {
			return cfg.GrokModel
		}
		return "grok"
	default:
		return cfg.OllamaModel
	}
}

// conversationRuntimeBuilder returns the RuntimeBuilder the TUI invokes on
// entering the conversation screen: assets are ensured with in-screen
// progress and the mic goes hot here, not in the launcher (D2). A non-nil
// resume session seeds both the brain history and the viewport.
func conversationRuntimeBuilder(resumeSession *session.Session) appTUI.RuntimeBuilder {
	return func(ctx context.Context, progress func(name string, pct float64), sessionID string) (*appTUI.ConversationRuntime, error) {
		// Reload config in case settings changed inside the TUI.
		cfg, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("reload config: %w", err)
		}

		req := config.AssetRequest{
			NeedTTS: !noVoice,
			NeedSTT: !textMode,
			NeedVAD: !textMode && cfg.VADEnabled,
		}
		if err := config.EnsureRuntimeAssets(ctx, cfg, req, progress); err != nil {
			return nil, fmt.Errorf("ensure runtime assets: %w", err)
		}

		bus := events.NewBus()
		p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice)
		if err != nil {
			return nil, fmt.Errorf("init pipeline: %w", err)
		}

		// Preload the model while the user reads the empty screen so their
		// first turn isn't the cold one. Best-effort — failures are ignored.
		if w, ok := p.Brain.(brain.Warmer); ok {
			go w.Warmup(ctx)
		}

		sess := resumeSession
		if sessionID != "" && (sess == nil || sess.ID != sessionID) {
			sess, err = session.Load(sessionID)
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("load session: %w", err)
			}
		}
		resumed := sess != nil
		if sess == nil {
			sess = session.New(cfg.BrainProvider, modelName(cfg))
		} else {
			p.Brain.LoadHistory(sess.Turns)
		}
		liveSpeaker := speaker.NewLiveAdapter(ctx, nil, 4)

		p.OnTurn = func() {
			if err := sess.Save(p.Brain.History()); err != nil {
				bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
			}
		}

		rt := &appTUI.ConversationRuntime{
			Pipeline:     p,
			Bus:          bus,
			Voice:        p.STT != nil,
			Output:       p.TTS != nil && p.Player != nil,
			SessionID:    sess.ID,
			InputDevice:  cfg.InputDevice,
			OutputDevice: cfg.OutputDevice,
			LiveSpeaker:  liveSpeaker,
			Cleanup: func() {
				_ = liveSpeaker.Close()
				if err := sess.Save(p.Brain.History()); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: failed to save session %s: %v\n", sess.ID, err)
				}
				cleanup()
			},
		}
		if resumed {
			rt.Seed = sess.Turns
		}
		return rt, nil
	}
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
	sess := resumeSession
	if sess == nil {
		sess = session.New(cfg.BrainProvider, modelName(cfg))
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
		player := audio.NewPlayerWithDevice(cfg.OutputDevice)
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

		if strings.EqualFold(strings.TrimSpace(cfg.TTSFallbackProvider), "kokoro") &&
			!strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "kokoro") {
			fallbackCfg := *cfg
			fallbackCfg.TTSProvider = "kokoro"
			fallbackProvider, fallbackCleanup, fallbackErr := tts.NewProvider(&fallbackCfg)
			if fallbackErr != nil {
				fmt.Fprintf(os.Stderr, "warning: Kokoro TTS fallback unavailable: %v\n", fallbackErr)
			} else {
				p.TTSFallback = fallbackProvider
				if fallbackCleanup != nil {
					cleanups = append(cleanups, fallbackCleanup)
				}
			}
		}
	}

	// Audio capture + VAD + STT (skip in text mode).
	if !text {
		var frontend *audio.VoiceFrontend
		if cfg.VoiceFrontendEnabled {
			frontend = audio.NewVoiceFrontend()
			cleanups = append(cleanups, func() { _ = frontend.Close() })
		}

		capture := audio.NewCaptureWithDevice(cfg.InputDevice)
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
