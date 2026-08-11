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
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/transcript"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
	"github.com/lancekrogers/samantha/internal/ui"
)

var (
	textMode          bool
	noVoice           bool
	skipTUI           bool
	debugAudioDir     string
	transcriptLogPath string
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

// personaOverride is --persona: a process-scoped identity override. It is
// deliberately not persisted — `samantha persona use` is the persisting path —
// so trying a persona never edits config.yaml behind the user's back.
var personaOverride string

func init() {
	rootCmd.Flags().BoolVarP(&textMode, "text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolVarP(&noVoice, "no-voice", "n", false, "Disable TTS output")
	rootCmd.Flags().BoolVar(&skipTUI, "no-tui", false, "Skip TUI launcher, start directly")
	rootCmd.Flags().StringVar(&personaOverride, "persona", "", "Run as this persona for this process only (never persisted; see `samantha persona use` to persist)")
	rootCmd.PersistentFlags().StringVar(&debugAudioDir, "debug-audio", "", "Record TTS source WAVs, exact device-output WAV, and callback timing (optional DIR)")
	rootCmd.PersistentFlags().Lookup("debug-audio").NoOptDefVal = "auto"
	rootCmd.PersistentFlags().StringVar(&transcriptLogPath, "transcript-log", "", "Append conversation events as JSONL to FILE (harnesses, field debugging)")
}

// attachTranscriptLog starts the JSONL event tap when --transcript-log is
// set. The returned closer is a no-op otherwise. A tap that cannot open is a
// startup error: a harness must never run blind believing it is recording.
func attachTranscriptLog(bus *events.Bus) (func(), error) {
	if transcriptLogPath == "" {
		return func() {}, nil
	}
	w, err := transcript.NewWriter(transcriptLogPath)
	if err != nil {
		return nil, err
	}
	w.Attach(bus)
	return func() { _ = w.Close() }, nil
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
	return func(ctx context.Context, progress func(name string, pct float64), sessionID, personaID string) (*appTUI.ConversationRuntime, error) {
		// Reload config in case settings changed inside the TUI.
		cfg, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("reload config: %w", err)
		}

		// Bind the session's identity now: the runtime is built from this
		// snapshot, so later persona switches/edits (Settings, Personas, other
		// sessions) cannot mutate this conversation's prompt, name, or voice.
		//
		// The *document body* behind that identity is re-read each turn, so an
		// edit to the bound persona lands on the next reply (R-P2). Those are
		// two different axes and only the first is pinned: switching to another
		// persona mid-session still cannot retarget this conversation. See
		// internal/brain/reresolve.go.
		if personaID == "" {
			personaID = personaOverride
		}
		binding, err := persona.ResolveBinding(cfg, personaID)
		if err != nil {
			return nil, fmt.Errorf("resolve persona: %w", err)
		}
		cfg = binding.Config()

		spCfg := speaker.FromAppConfig(cfg)
		req := config.AssetRequest{
			NeedTTS:     !noVoice,
			NeedSTT:     !textMode,
			NeedVAD:     !textMode && cfg.VADEnabled,
			NeedSpeaker: !textMode && spCfg.LiveActive(),
		}
		if err := config.EnsureRuntimeAssets(ctx, cfg, req, progress); err != nil {
			return nil, fmt.Errorf("ensure runtime assets: %w", err)
		}

		bus := events.NewBus()
		closeTap, err := attachTranscriptLog(bus)
		if err != nil {
			return nil, fmt.Errorf("transcript log: %w", err)
		}
		p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice)
		if err != nil {
			closeTap()
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
				closeTap()
				return nil, fmt.Errorf("load session: %w", err)
			}
		}
		resumed := sess != nil
		if sess == nil {
			sess = session.New(cfg.BrainProvider, modelName(cfg))
		} else {
			p.Brain.LoadHistory(sess.Turns)
		}

		var captureSrc speaker.CaptureSource
		if p.Capture != nil {
			captureSrc = p.Capture
		}
		liveSpeaker, stopLive, liveDetail := prepareLiveSpeaker(ctx, cfg, captureSrc, progress)
		if liveDetail != "" {
			bus.Emit(events.Info{Message: liveDetail})
		}
		liveTTS := &liveTTSManager{}

		// Session-local rename table: UI bubbles + model prompt attribution.
		speakerNames := speaker.NewNameMap()
		brain.AttachSpeakerNames(p.Brain, speakerNames)
		if liveSpeaker != nil {
			liveCfg := speaker.FromAppConfig(cfg).Normalize()
			if liveCfg.LiveActive() && liveCfg.Live.Mode == speaker.LiveModeOwnerVerify {
				p.SpeakerGate = ownerVerifyGate(liveSpeaker.Stats)
			}
		}
		p.CurrentSpeaker = func() string {
			if liveSpeaker == nil {
				return ""
			}
			st := liveSpeaker.Stats()
			switch st.Status {
			case speaker.LiveHealthy, speaker.LiveRunning, speaker.LiveDegraded:
				return strings.TrimSpace(st.LastLabel)
			default:
				return ""
			}
		}

		p.OnTurn = func() {
			if err := sess.Save(p.Brain.History()); err != nil {
				bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
			}
		}
		wireCompact(p, cfg, sess, bus)

		// Capture the session persona id so Settings → ReloadVoice cannot swap
		// this conversation onto a different identity's voice (e.g. uncle-fu
		// mid-session must not become samantha/kokoro just because that is the
		// live active_persona on disk).
		sessionPersonaID := binding.PersonaID

		// Declare first so ReloadVoice can update SessionCfg after install.
		var rt *appTUI.ConversationRuntime
		rt = &appTUI.ConversationRuntime{
			Pipeline:     p,
			Bus:          bus,
			Voice:        p.STT != nil,
			Output:       p.HasTTS() && p.Player != nil,
			PersonaID:    binding.PersonaID,
			AgentName:    binding.AgentName,
			SessionCfg:   binding.Config(),
			SessionID:    sess.ID,
			InputDevice:  cfg.InputDevice,
			OutputDevice: cfg.OutputDevice,
			LiveSpeaker:  liveSpeaker,
			SpeakerNames: speakerNames,
			ReloadVoice: func(reloadCtx context.Context) error {
				if noVoice {
					return nil
				}
				reloaded, err := config.Load()
				if err != nil {
					return fmt.Errorf("reload config: %w", err)
				}
				// Re-apply the session persona on top of the reloaded app config
				// so device/settings changes take effect without replacing the
				// bound voice identity.
				rebound, err := persona.ResolveBinding(reloaded, sessionPersonaID)
				if err != nil {
					return fmt.Errorf("resolve session persona: %w", err)
				}
				sessionCfg := rebound.Config()
				if err := config.EnsureRuntimeAssets(reloadCtx, sessionCfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
					return fmt.Errorf("ensure TTS assets: %w", err)
				}
				set, err := newTTSProviderSet(sessionCfg)
				if err != nil {
					return fmt.Errorf("init TTS: %w", err)
				}
				if reloadCtx.Err() != nil || !liveTTS.install(p, set) {
					set.Close()
					if err := reloadCtx.Err(); err != nil {
						return err
					}
					return fmt.Errorf("conversation is shutting down")
				}
				// SessionCfg is only published from ReloadVoice after a successful
				// install and is consumed on the TUI update thread after
				// voiceReloadedMsg — do not read it from other goroutines.
				rt.SessionCfg = sessionCfg
				if set.FallbackWarning != nil {
					bus.Emit(events.Error{Stage: "tts-fallback", Message: set.FallbackWarning.Error()})
				}
				return nil
			},
			Cleanup: func() {
				// stopLive closes feed, adapter, and analyzer/engine.
				if stopLive != nil {
					stopLive()
				}
				if err := sess.Save(p.Brain.History()); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: failed to save session %s: %v\n", sess.ID, err)
				}
				liveTTS.Close()
				cleanup()
				closeTap()
			},
		}
		if resumed {
			rt.Seed = sess.Turns
		}
		return rt, nil
	}
}

// wireCompact equips the pipeline for /compact: the resolved summarize
// instruction and a pre-compact session backup. A prompt-resolution failure
// disables /compact with a visible error instead of blocking startup.
func wireCompact(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session, bus *events.Bus) {
	prompt, err := brain.CompactInstruction(cfg)
	if err != nil {
		bus.Emit(events.Error{Stage: "compact", Message: fmt.Sprintf("compact prompt: %v (/compact disabled)", err)})
		return
	}
	p.CompactPrompt = prompt
	p.OnCompactBackup = func(turns []brain.Turn) {
		if err := sess.SaveBackup(turns); err != nil {
			bus.Emit(events.Error{Stage: "compact", Message: fmt.Sprintf("pre-compact backup: %v", err)})
		}
	}
}

func startPipeline(cfg *config.Config, resumeSession *session.Session) error {
	ctx, cancel := signalContext()
	defer cancel()

	// Same binding rule as the TUI runtime: the CLI conversation runs on an
	// identity snapshot, not the live global overlay. --persona selects which
	// identity, for this process only.
	binding, err := persona.ResolveBinding(cfg, personaOverride)
	if err != nil {
		return fmt.Errorf("resolve persona: %w", err)
	}
	cfg = binding.Config()

	req := config.AssetRequest{
		NeedTTS: !noVoice,
		NeedSTT: !textMode,
		NeedVAD: !textMode && cfg.VADEnabled,
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	closeTap, err := attachTranscriptLog(bus)
	if err != nil {
		return fmt.Errorf("transcript log: %w", err)
	}
	defer closeTap()
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

		ttsSet, err := newTTSProviderSet(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init TTS: %w", err)
		}
		cleanups = append(cleanups, ttsSet.Close)
		p.ReplaceTTS(ttsSet.Primary, ttsSet.Fallback)
		if ttsSet.FallbackWarning != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", ttsSet.FallbackWarning)
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
