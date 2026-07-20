package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/session"
)

type screen int

const (
	screenLauncher screen = iota
	screenSettings
	screenConversation
	screenSessions
	screenMeetingSetup
	screenMeeting
	screenAudiobook
	screenPickBook
	screenRemote
)

// App is the top-level bubbletea model.
type App struct {
	screen    screen
	cfg       *config.Config
	providers []discovery.ProviderInfo
	width     int
	height    int

	launcher     launcherModel
	settings     settingsModel
	conversation conversationModel
	sessions     sessionsModel
	meetingSetup meetingSetupModel
	meeting      meetingModel
	audiobook    audiobookModel
	pickBook     pickBookModel
	remote       remoteModel

	// Conversation runtime wiring, set by Run before the program starts.
	builder  RuntimeBuilder
	runCtx   context.Context
	wg       *sync.WaitGroup
	progress *eventBridge
	// slot owns the built runtime for shutdown cleanup even when a ready
	// message is dropped because the user quit mid-build.
	slot *runtimeSlot

	// Meeting recorder wiring (launcher → setup → recorder).
	meetingBuilder MeetingBuilder
	meetingRT      *MeetingRuntime

	// Set once the conversation runtime is built; Run tears it down after
	// the program exits.
	runtime  *ConversationRuntime
	fatalErr error

	// Settings can be opened from the launcher or from a live conversation.
	// Keep the origin so Esc/q returns to the screen the user came from.
	settingsReturnScreen screen
	settingsResumeVoice  bool

	// startInConversation skips the launcher (resume/continue).
	startInConversation bool

	quitting bool
}

// NewApp creates the TUI application.
func NewApp(cfg *config.Config) App {
	providers := discovery.DiscoverProviders(cfg)
	savedSessions := resumableSessions(session.List())
	conversation := newConversation(cfg.AgentName)
	conversation.cfg = cfg

	return App{
		screen:       screenLauncher,
		cfg:          cfg,
		providers:    providers,
		launcher:     newLauncher(cfg, providers, savedSessions),
		settings:     newSettings(cfg, providers),
		conversation: conversation,
		sessions:     newSessions(savedSessions),
		audiobook:    newAudiobook(cfg),
		pickBook:     newPickBook(cfg),
	}
}

func (a App) Init() tea.Cmd {
	if a.startInConversation {
		return func() tea.Msg { return startPipelineMsg{} }
	}
	return nil
}

// switchScreen is a message to change screens.
type switchScreenMsg screen

// settingsDoneMsg returns from settings to the screen that opened it.
type settingsDoneMsg struct{}

// startPipelineMsg enters the conversation screen and builds the pipeline
// there (D2) — the TUI no longer exits to hand off.
type startPipelineMsg struct{ sessionID string }

// quitMsg signals the app should exit.
type quitMsg struct{}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && a.screen == screenConversation && a.conversation.editor.selectionActive() {
			a.conversation.copySelection()
			return a, nil
		}
		// Meeting owns Ctrl+C as "stop recording" (returns to launcher).
		if msg.String() == "ctrl+c" && (a.screen == screenMeeting || a.screen == screenMeetingSetup) {
			break // fall through to screen Update
		}
		if msg.String() == "ctrl+c" {
			a.settings.closePreview()
			a.remote.stop()
			if err := a.stopMeetingRuntime(); err != nil {
				a.fatalErr = errors.Join(a.fatalErr, err)
			}
			a.quitting = true
			return a, tea.Quit
		}

	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		// Keep every screen's geometry current so switching into Sessions /
		// Settings / etc. does not render with height=0 (which capped the
		// sessions list at 3 rows).
		a.launcher, _ = a.launcher.Update(msg)
		a.settings, _ = a.settings.Update(msg)
		a.sessions, _ = a.sessions.Update(msg)
		a.meetingSetup, _ = a.meetingSetup.Update(msg)
		a.pickBook, _ = a.pickBook.Update(msg)
		a.remote, _ = a.remote.Update(msg)
		if a.screen != screenConversation {
			a.conversation, _ = a.conversation.Update(msg)
		}
		// Meeting is updated via the fall-through delegate (typed tea.Model).

	case switchScreenMsg:
		target := screen(msg)
		var pauseVoice tea.Cmd
		if target == screenSettings {
			a.settingsReturnScreen = a.screen
			a.settingsResumeVoice = a.screen == screenConversation && a.conversation.voiceEnabled
			if a.settingsReturnScreen == screenConversation {
				pauseVoice = a.conversation.setInputMuted(true)
			}
		}
		if a.screen == screenSettings {
			a.settings.closePreview()
		}
		if a.screen == screenRemote && target != screenRemote {
			a.remote.stop()
		}
		var leaveMeetingErr error
		if a.screen == screenMeeting && target != screenMeeting {
			leaveMeetingErr = a.stopMeetingRuntime()
		}
		prev := a.screen
		a.screen = target
		if leaveMeetingErr != nil && target == screenLauncher {
			a.launcher = a.launcher.withBanner(leaveMeetingErr.Error(), true)
		}
		switch a.screen {
		case screenSettings:
			// Replacing the model must not orphan an in-flight preview or player.
			a.settings.closePreview()
			a.settings = newSettings(a.cfg, a.providers)
			return a, tea.Batch(a.settings.loadDevices(), pauseVoice)
		case screenSessions:
			// Apply stored geometry immediately — Bubble Tea only re-emits
			// WindowSize on actual resize, not on screen switches.
			a.sessions.width, a.sessions.height = a.width, a.height
			a.sessions.ensureVisible()
		case screenMeetingSetup:
			a.meetingSetup = newMeetingSetup()
			a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
		case screenAudiobook:
			// Preserve form state when returning from the library picker.
			if prev != screenPickBook {
				a.audiobook = newAudiobook(a.cfg)
			}
		case screenPickBook:
			a.pickBook = newPickBook(a.cfg)
			a.pickBook.width, a.pickBook.height = a.width, a.height
		case screenRemote:
			a.remote = newRemote(a.runCtx, nil)
			a.remote.width, a.remote.height = a.width, a.height
			return a, a.remote.start()
		}
		return a, nil

	case startMeetingMsg:
		a.screen = screenMeeting
		a.meeting = newEmbeddedMeeting()
		a.meeting.width, a.meeting.height = a.width, a.height
		a.meeting.reflow()
		a.meeting.status = "Preparing models…"
		return a, buildMeeting(a.meetingBuilder, a.runCtx, msg.Description)

	case meetingReadyMsg:
		if msg.err != nil {
			a.screen = screenMeetingSetup
			a.meetingSetup = newMeetingSetup()
			a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
			a.meetingSetup.err = msg.err.Error()
			return a, nil
		}
		a.meetingRT = msg.rt
		// Child cancel stops the listen loop without ending the whole App.
		mctx, mcancel := context.WithCancel(a.runCtx)
		cmd := a.meeting.beginRecording(MeetingOpts{
			Ctx:         mctx,
			Cancel:      mcancel,
			Capture:     msg.rt.Capture,
			Provider:    msg.rt.Provider,
			Writer:      msg.rt.Writer,
			Description: msg.rt.Description,
			Path:        msg.rt.Path,
			StopPhrases: msg.rt.StopPhrases,
			Embedded:    true,
		})
		return a, cmd

	case meetingDoneMsg:
		closeErr := a.stopMeetingRuntime()
		a.screen = screenLauncher
		if err := errors.Join(msg.Err, closeErr); err != nil {
			a.launcher = a.launcher.withBanner(fmt.Sprintf("Meeting ended with error: %v", err), true)
		}
		return a, nil

	case bookPickedMsg:
		a.audiobook.input = msg.path
		a.audiobook.errText = ""
		a.audiobook.message = "Filled input from Calibre library"
		a.audiobook.command = ""
		a.screen = screenAudiobook
		return a, nil

	case settingsDoneMsg:
		a.settings.closePreview()
		target := a.settingsReturnScreen
		if target == screenSettings {
			target = screenLauncher
		}
		a.screen = target
		resumeVoice := target == screenConversation && a.settingsResumeVoice
		a.settingsResumeVoice = false
		if resumeVoice {
			return a, a.conversation.setInputMuted(false)
		}
		return a, nil

	case startPipelineMsg:
		a.settings.closePreview()
		if a.builder == nil {
			// No runtime wiring (tests): nothing to build.
			return a, nil
		}
		a.screen = screenConversation
		a.conversation.setStatus("Preparing...", false)
		return a, tea.Batch(a.progress.wait(), buildRuntime(a.builder, a.runCtx, a.progress, a.slot, msg.sessionID))

	case assetProgressMsg:
		a.conversation.setStatus(formatAssetProgress(msg), false)
		return a, a.progress.wait()

	case progressClosedMsg:
		return a, nil

	case runtimeReadyMsg:
		if msg.err != nil {
			a.fatalErr = msg.err
			a.quitting = true
			return a, tea.Quit
		}
		// Build finished after quit: slot already cleaned (or will be in run).
		if a.quitting || a.runCtx.Err() != nil {
			return a, nil
		}
		a.runtime = msg.rt
		a.conversation.setStatus("", false)
		a.conversation.seedTranscript(msg.rt.Seed)
		cmd := a.conversation.startConversation(conversationDeps{
			runner:         msg.rt.Pipeline,
			bus:            msg.rt.Bus,
			clearHistory:   msg.rt.Pipeline.Brain.ClearHistory,
			voice:          msg.rt.Voice,
			output:         msg.rt.Output,
			setOutputMuted: msg.rt.Pipeline.SetOutputMuted,
			sessionID:      msg.rt.SessionID,
			inputDevice:    msg.rt.InputDevice,
			outputDevice:   msg.rt.OutputDevice,
			ctx:            a.runCtx,
			wg:             a.wg,
		})
		return a, cmd

	case quitMsg:
		a.remote.stop()
		a.quitting = true
		return a, tea.Quit
	}

	// Delegate to active screen.
	var cmd tea.Cmd
	switch a.screen {
	case screenLauncher:
		a.launcher, cmd = a.launcher.Update(msg)
	case screenSettings:
		a.settings, cmd = a.settings.Update(msg)
	case screenConversation:
		a.conversation, cmd = a.conversation.Update(msg)
	case screenSessions:
		a.sessions, cmd = a.sessions.Update(msg)
	case screenMeetingSetup:
		a.meetingSetup, cmd = a.meetingSetup.Update(msg)
	case screenMeeting:
		var m tea.Model
		m, cmd = a.meeting.Update(msg)
		a.meeting = m.(meetingModel)
	case screenAudiobook:
		a.audiobook, cmd = a.audiobook.Update(msg)
	case screenPickBook:
		a.pickBook, cmd = a.pickBook.Update(msg)
	case screenRemote:
		a.remote, cmd = a.remote.Update(msg)
	}

	return a, cmd
}

func (a App) View() string {
	switch a.screen {
	case screenLauncher:
		return a.launcher.View()
	case screenSettings:
		return a.settings.View()
	case screenConversation:
		return a.conversation.View()
	case screenSessions:
		return a.sessions.View()
	case screenMeetingSetup:
		return a.meetingSetup.View()
	case screenMeeting:
		return a.meeting.View()
	case screenAudiobook:
		return a.audiobook.View()
	case screenPickBook:
		return a.pickBook.View()
	case screenRemote:
		return a.remote.View()
	default:
		return ""
	}
}

// stopMeetingRuntime cancels the listen loop, writes the dual-log trailer, and
// releases STT resources. Returns any Writer.Close failure so callers can
// surface a silent trailer/session_end write problem (files may already hold
// synced events). Idempotent when no runtime is active.
func (a *App) stopMeetingRuntime() error {
	if a.meetingRT == nil {
		return nil
	}
	if a.meeting.opts.Cancel != nil {
		a.meeting.opts.Cancel()
	}
	var closeErr error
	if a.meetingRT.Writer != nil {
		if _, err := a.meetingRT.Writer.Close(); err != nil {
			closeErr = fmt.Errorf("close meeting log: %w", err)
		}
	}
	if a.meetingRT.Cleanup != nil {
		a.meetingRT.Cleanup()
	}
	a.meetingRT = nil
	return closeErr
}

// Run starts the TUI as one continuous program: launcher, settings, and the
// live conversation all run inside it. The pipeline is built lazily on
// entering the conversation screen (D2) and torn down here after the program
// exits.
func Run(cfg *config.Config, build RuntimeBuilder) error {
	return RunWithMeeting(cfg, build, nil)
}

// RunWithMeeting is Run plus an optional MeetingBuilder for the launcher
// "Record meeting" entry.
func RunWithMeeting(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder) error {
	return run(cfg, build, meeting, false)
}

// RunConversation starts the TUI directly in the conversation screen —
// resume/continue land in the live conversation, not the launcher.
func RunConversation(cfg *config.Config, build RuntimeBuilder) error {
	return run(cfg, build, nil, true)
}

func run(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder, startInConversation bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := NewApp(cfg)
	app.builder = build
	app.meetingBuilder = meeting
	app.startInConversation = startInConversation
	app.runCtx = ctx
	app.wg = &sync.WaitGroup{}
	app.progress = newEventBridge(16)
	app.slot = &runtimeSlot{}

	// Native libraries write directly to file descriptor 2, bypassing Bubble
	// Tea and corrupting the terminal surface. Keep those diagnostics in a log;
	// debug-audio runs colocate it with the capture bundle.
	//
	// Trade-off: restoreDiagnostics only runs via this goroutine's defers, so
	// an unrecovered panic on a different goroutine (a pipeline or TTS worker,
	// for instance) still crashes with fd 2 pointed at the log file — its
	// stack trace lands in native-diagnostics.log instead of the terminal.
	diagnosticsDir := filepath.Join(config.ConfigDir(), "logs")
	if debugDir := audio.DebugAudioDir(); debugDir != "" {
		diagnosticsDir = debugDir
	}
	restoreDiagnostics, err := redirectNativeDiagnostics(filepath.Join(diagnosticsDir, "native-diagnostics.log"))
	if err != nil {
		return fmt.Errorf("redirect native diagnostics: %w", err)
	}
	defer func() { _ = restoreDiagnostics() }()

	// Force dark + truecolor before Bubble Tea can issue OSC queries that
	// hang or mis-detect on bare PTYs (VHS/ttyd). Same pattern as festival.
	forceTUIColorProfile()

	// Do not enable Bubble Tea mouse reporting here. Claiming the mouse makes
	// terminals send clicks and drags to Samantha instead of allowing native
	// text selection, copy, and link activation.
	p := tea.NewProgram(app, tea.WithAltScreen())
	m, runErr := p.Run()
	final, _ := m.(App)
	if final.remote.server != nil {
		final.remote.stopAndWait(remoteStopTimeout)
	} else {
		app.remote.stopAndWait(remoteStopTimeout)
	}

	// Stop the in-flight turn, drain it, then tear the pipeline down — the
	// same order app.Run's defer chain guarantees on the non-TTY path.
	cancel()
	waitTimeout(app.wg, drainTimeout)

	// Prefer the slot: it still holds a runtime if build finished after quit
	// and the ready message never reached Update. final.runtime is only set
	// when Update applied runtimeReadyMsg.
	if final.slot != nil {
		final.slot.cleanup()
	} else if app.slot != nil {
		app.slot.cleanup()
	}

	// Meeting recorder may still be open if the program quit mid-session
	// without a meetingDoneMsg (e.g. outer Quit). Idempotent when already closed.
	if err := final.stopMeetingRuntime(); err != nil {
		final.fatalErr = errors.Join(final.fatalErr, err)
	}

	if runErr != nil {
		return fmt.Errorf("TUI error: %w", runErr)
	}
	return final.fatalErr
}
