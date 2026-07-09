package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
)

type screen int

const (
	screenLauncher screen = iota
	screenSettings
	screenConversation
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
	audiobook    audiobookModel

	// Set after settings are saved to signal pipeline should start.
	startPipeline bool
	quitting      bool
}

// NewApp creates the TUI application.
func NewApp(cfg *config.Config) App {
	providers := discovery.DiscoverProviders(cfg)

	return App{
		screen:    screenLauncher,
		cfg:       cfg,
		providers: providers,
		launcher:  newLauncher(cfg, providers),
		settings:  newSettings(cfg, providers),
		audiobook: newAudiobook(cfg),
	}
}

func (a App) Init() tea.Cmd {
	return nil
}

// switchScreen is a message to change screens.
type switchScreenMsg screen

// startPipelineMsg signals the TUI should exit and start the voice pipeline.
type startPipelineMsg struct{}

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
		case screenAudiobook:
			a.audiobook = newAudiobook(a.cfg)
		}
		return a, nil

	case startPipelineMsg:
		a.settings.closePreview()
		a.startPipeline = true
		return a, tea.Quit

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
	case screenAudiobook:
		return a.audiobook.View()
	default:
		return ""
	}
}

// ShouldStartPipeline returns true if the TUI exited to start voice mode.
func (a App) ShouldStartPipeline() bool {
	return a.startPipeline
}

// Run starts the TUI and returns whether the pipeline should start.
func Run(cfg *config.Config) (bool, error) {
	app := NewApp(cfg)
	p := tea.NewProgram(app, tea.WithAltScreen())

	m, err := p.Run()
	if err != nil {
		return false, fmt.Errorf("TUI error: %w", err)
	}

	final := m.(App)
	return final.ShouldStartPipeline(), nil
}
