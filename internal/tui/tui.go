package tui

import (
	"context"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

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
	screenAudiobook
)

// App is the top-level bubbletea model.
type App struct {
	screen    screen
	cfg       *config.Config
	providers []discovery.ProviderInfo

	launcher     launcherModel
	settings     settingsModel
	conversation conversationModel
	sessions     sessionsModel
	audiobook    audiobookModel

	// Conversation runtime wiring, set by Run before the program starts.
	builder  RuntimeBuilder
	runCtx   context.Context
	wg       *sync.WaitGroup
	progress *eventBridge
	// slot owns the built runtime for shutdown cleanup even when a ready
	// message is dropped because the user quit mid-build.
	slot *runtimeSlot

	// Set once the conversation runtime is built; Run tears it down after
	// the program exits.
	runtime  *ConversationRuntime
	fatalErr error

	// startInConversation skips the launcher (resume/continue).
	startInConversation bool

	quitting bool
}

// NewApp creates the TUI application.
func NewApp(cfg *config.Config) App {
	providers := discovery.DiscoverProviders(cfg)
	savedSessions := resumableSessions(session.List())

	return App{
		screen:       screenLauncher,
		cfg:          cfg,
		providers:    providers,
		launcher:     newLauncher(cfg, providers, savedSessions),
		settings:     newSettings(cfg, providers),
		conversation: newConversation(cfg.AgentName),
		sessions:     newSessions(savedSessions),
		audiobook:    newAudiobook(cfg),
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

// startPipelineMsg enters the conversation screen and builds the pipeline
// there (D2) — the TUI no longer exits to hand off.
type startPipelineMsg struct{ sessionID string }

// quitMsg signals the app should exit.
type quitMsg struct{}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			a.settings.closePreview()
			a.quitting = true
			return a, tea.Quit
		}

	case tea.WindowSizeMsg:
		// The conversation screen needs dimensions even while another screen
		// is active, or entering it later renders at zero size.
		if a.screen != screenConversation {
			a.conversation, _ = a.conversation.Update(msg)
		}

	case switchScreenMsg:
		if a.screen == screenSettings {
			a.settings.closePreview()
		}
		a.screen = screen(msg)
		switch a.screen {
		case screenSettings:
			// Replacing the model must not orphan an in-flight preview or player.
			a.settings.closePreview()
			a.settings = newSettings(a.cfg, a.providers)
			return a, a.settings.loadDevices()
		case screenAudiobook:
			a.audiobook = newAudiobook(a.cfg)
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
	case screenAudiobook:
		a.audiobook, cmd = a.audiobook.Update(msg)
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
	case screenAudiobook:
		return a.audiobook.View()
	default:
		return ""
	}
}

// Run starts the TUI as one continuous program: launcher, settings, and the
// live conversation all run inside it. The pipeline is built lazily on
// entering the conversation screen (D2) and torn down here after the program
// exits.
func Run(cfg *config.Config, build RuntimeBuilder) error {
	return run(cfg, build, false)
}

// RunConversation starts the TUI directly in the conversation screen —
// resume/continue land in the live conversation, not the launcher.
func RunConversation(cfg *config.Config, build RuntimeBuilder) error {
	return run(cfg, build, true)
}

func run(cfg *config.Config, build RuntimeBuilder, startInConversation bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := NewApp(cfg)
	app.builder = build
	app.startInConversation = startInConversation
	app.runCtx = ctx
	app.wg = &sync.WaitGroup{}
	app.progress = newEventBridge(16)
	app.slot = &runtimeSlot{}

	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m, runErr := p.Run()

	// Stop the in-flight turn, drain it, then tear the pipeline down — the
	// same order app.Run's defer chain guarantees on the non-TTY path.
	cancel()
	waitTimeout(app.wg, drainTimeout)

	// Prefer the slot: it still holds a runtime if build finished after quit
	// and the ready message never reached Update. final.runtime is only set
	// when Update applied runtimeReadyMsg.
	final, _ := m.(App)
	if final.slot != nil {
		final.slot.cleanup()
	} else if app.slot != nil {
		app.slot.cleanup()
	}

	if runErr != nil {
		return fmt.Errorf("TUI error: %w", runErr)
	}
	return final.fatalErr
}
