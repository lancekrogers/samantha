package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/discovery"
	"github.com/lancekrogers/samantha/internal/meeting"
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
	screenMeetingResults
	screenMeetingRoute
	screenAudiobook
	screenPickBook
	screenLibrary
	screenRemote
	screenPersonas
)

// meetingRoutePlan is the per-session routing choice from the start picker.
type meetingRoutePlan struct {
	Kind string // local | ask | dest | empty (use config mode)
	Dest meeting.Destination
}

// App is the top-level bubbletea model.
type App struct {
	screen    screen
	cfg       *config.Config
	providers []discovery.ProviderInfo
	width     int
	height    int

	launcher       launcherModel
	settings       settingsModel
	conversation   conversationModel
	sessions       sessionsModel
	meetingSetup   meetingSetupModel
	meeting        meetingModel
	meetingResults meetingResultsModel
	meetingRoute   meetingRouteModel
	audiobook      audiobookModel
	pickBook       pickBookModel
	library        libraryModel
	remote         remoteModel
	personas       personasModel

	// Conversation runtime wiring, set by Run before the program starts.
	builder  RuntimeBuilder
	runCtx   context.Context
	wg       *sync.WaitGroup
	progress *eventBridge
	// slot owns the built runtime for shutdown cleanup even when a ready
	// message is dropped because the user quit mid-build.
	slot *runtimeSlot

	// Meeting recorder wiring (launcher → setup → recorder).
	meetingBuilder   MeetingBuilder
	meetingRT        *MeetingRuntime
	meetingRoutePlan meetingRoutePlan // chosen at setup; consumed after stop

	// Set once the conversation runtime is built; Run tears it down after
	// the program exits.
	runtime  *ConversationRuntime
	fatalErr error

	// Settings can be opened from the launcher or from a live conversation.
	// Keep the origin so Esc/q returns to the screen the user came from.
	settingsReturnScreen screen
	settingsResumeVoice  bool
	settingsVoiceState   string

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
		library:      newLibrary(cfg),
		personas:     newPersonas(cfg),
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
			if a.screen == screenPickBook {
				a.pickBook.cancelResolve()
			}
			if a.screen == screenLibrary {
				a.library.cancelPrepare()
			}
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
		a.library, _ = a.library.Update(msg)
		a.remote, _ = a.remote.Update(msg)
		a.personas, _ = a.personas.Update(msg)
		if a.screen != screenConversation {
			a.conversation, _ = a.conversation.Update(msg)
		}
		// Meeting is updated via the fall-through delegate (typed tea.Model).

	case switchScreenMsg:
		target := screen(msg)
		if a.screen == screenPickBook && target != screenPickBook {
			a.pickBook.cancelResolve()
		}
		if a.screen == screenLibrary && target != screenLibrary {
			a.library.cancelPrepare()
		}
		var pauseVoice tea.Cmd
		if target == screenSettings {
			a.settingsReturnScreen = a.screen
			a.settingsResumeVoice = a.screen == screenConversation && a.conversation.voiceEnabled
			a.settingsVoiceState = voiceSettingsState(a.cfg)
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
			// Apply stored geometry immediately — Bubble Tea only re-emits
			// WindowSize on actual resize, not on screen switches. Without
			// this, the list falls back to a default height and 80-col width
			// even in a large terminal split. Live resizes still flow through
			// WindowSizeMsg while settings is open.
			a.settings.width, a.settings.height = a.width, a.height
			a.settings.ensureCursorVisible()
			return a, tea.Batch(a.settings.loadDevices(), pauseVoice)
		case screenSessions:
			// Apply stored geometry immediately — Bubble Tea only re-emits
			// WindowSize on actual resize, not on screen switches.
			a.sessions.width, a.sessions.height = a.width, a.height
			a.sessions.ensureVisible()
		case screenMeetingSetup:
			a.meetingSetup = newMeetingSetup(a.cfg)
			a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
		case screenAudiobook:
			// Preserve form state when returning from the library picker.
			if prev != screenPickBook {
				a.audiobook = newAudiobook(a.cfg)
			}
		case screenPickBook:
			a.pickBook = newPickBook(a.cfg)
			a.pickBook.width, a.pickBook.height = a.width, a.height
			return a, a.pickBook.runBrowse()
		case screenLibrary:
			a.library = newLibrary(a.cfg)
			a.library.width, a.library.height = a.width, a.height
			return a, a.library.InitCmd()
		case screenRemote:
			a.remote = newRemote(a.runCtx, nil)
			a.remote.width, a.remote.height = a.width, a.height
			return a, a.remote.start()
		case screenPersonas:
			a.personas = newPersonas(a.cfg)
			a.personas.width, a.personas.height = a.width, a.height
			a.personas.ensureVisible()
		case screenLauncher:
			// Refresh menu hints (active persona name) after Personas/Settings.
			if prev == screenPersonas || prev == screenSettings {
				saved := resumableSessions(session.List())
				a.launcher = newLauncher(a.cfg, a.providers, saved)
				a.launcher.width, a.launcher.height = a.width, a.height
			}
		}
		return a, nil

	case startMeetingMsg:
		a.meetingRoutePlan = meetingRoutePlan{
			Kind: msg.RoutePlan,
			Dest: msg.Destination,
		}
		a.screen = screenMeeting
		a.meeting = newEmbeddedMeeting()
		a.meeting.width, a.meeting.height = a.width, a.height
		a.meeting.reflow()
		a.meeting.status = "Preparing models…"
		return a, buildMeeting(a.meetingBuilder, a.runCtx, msg.Description)

	case meetingReadyMsg:
		if msg.err != nil {
			a.screen = screenMeetingSetup
			a.meetingSetup = newMeetingSetup(a.cfg)
			a.meetingSetup.width, a.meetingSetup.height = a.width, a.height
			a.meetingSetup.err = msg.err.Error()
			return a, nil
		}
		a.meetingRT = msg.rt
		finalizeSpeakers := msg.rt.FinalizeSpeakers
		speakerStatus, speakerError := msg.rt.SpeakerStatus, msg.rt.SpeakerError
		if demoMeetingSpeakersEnabled() && finalizeSpeakers == nil {
			finalizeSpeakers = demoMeetingSpeakerFinalizer(msg.rt.Writer, msg.rt.Path)
			speakerStatus = meeting.AnalysisQueued
			speakerError = "scripted multi-speaker fixture"
		}
		// Child cancel stops the listen loop without ending the whole App.
		mctx, mcancel := context.WithCancel(a.runCtx)
		cmd := a.meeting.beginRecording(MeetingOpts{
			Ctx:              mctx,
			Cancel:           mcancel,
			Capture:          msg.rt.Capture,
			Provider:         msg.rt.Provider,
			Writer:           msg.rt.Writer,
			Description:      msg.rt.Description,
			Path:             msg.rt.Path,
			StopPhrases:      msg.rt.StopPhrases,
			SpeakerStatus:    speakerStatus,
			SpeakerError:     speakerError,
			FinalizeSpeakers: finalizeSpeakers,
			Embedded:         true,
		})
		return a, cmd

	case meetingDoneMsg:
		summary, closeErr := a.stopMeetingRuntimeWithSummary()
		if err := errors.Join(msg.Err, closeErr); err != nil {
			a.screen = screenLauncher
			a.launcher = a.launcher.withBanner(fmt.Sprintf("Meeting ended with error: %v", err), true)
			return a, nil
		}
		// Keep the completed meeting visible for review. Routing continues only
		// after the user has seen the attributed transcript.
		a.meetingResults = newMeetingResults(summary)
		a.meetingResults.width, a.meetingResults.height = a.width, a.height
		a.meetingResults.resize()
		a.screen = screenMeetingResults
		return a, nil

	case meetingResultsDoneMsg:
		// Post-meeting routing: ask / auto / off / start-picker plan.
		// beginMeetingRoute may switch screens or return asynchronous work.
		prevScreen := a.screen
		if cmd := a.beginMeetingRoute(msg.summary); cmd != nil {
			if a.screen == prevScreen {
				a.screen = screenLauncher
				a.launcher = a.launcher.withBanner("Routing meeting notes…", false)
			}
			return a, cmd
		}
		if a.screen != prevScreen {
			return a, nil
		}
		a.screen = screenLauncher
		a.launcher = a.launcher.withBanner("Meeting saved: "+msg.summary.Bundle, false)
		return a, nil

	case meetingRouteReadyMsg:
		a.applyMeetingRouteReady(msg)
		return a, nil

	case meetingRouteResultMsg:
		a.screen = screenLauncher
		if msg.Banner != "" {
			a.launcher = a.launcher.withBanner(msg.Banner, msg.IsErr)
		}
		return a, nil

	case bookPickedMsg:
		// Results only belong to the active picker screen. This prevents a late
		// conversion from changing the audiobook form after the user left, and
		// the request ID prevents an older result from winning after a reload.
		if a.screen != screenPickBook || msg.requestID != a.pickBook.requestID {
			return a, nil
		}
		a.pickBook.cancelResolve()
		if msg.err != nil {
			a.pickBook.errText = msg.err.Error()
			a.pickBook.message = ""
			return a, nil
		}
		a.pickBook.loadPhase = pickIdle
		a.audiobook.input = msg.path
		a.audiobook.errText = ""
		a.audiobook.message = "Filled input from Calibre library"
		a.audiobook.command = ""
		a.screen = screenAudiobook
		return a, nil

	case libraryAudiobookMsg:
		// Ignore stale prepare results (user left or started another book).
		if a.screen != screenLibrary || msg.requestID != a.library.requestID {
			return a, nil
		}
		a.library.cancelPrepare()
		if msg.err != nil {
			if a.library.pane == libPaneDetail {
				a.library.detailErr = msg.err.Error()
			} else {
				a.library.errText = msg.err.Error()
			}
			a.library.message = ""
			return a, nil
		}
		// Preserve a clean audiobook form, then fill the path from the library.
		a.audiobook = newAudiobook(a.cfg)
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
		// Unstick cancel/response state if turn-done msgs were dropped while
		// Settings was active (conversation is not the focused screen).
		a.conversation.recoverTurnState()
		voiceChanged := a.settingsVoiceState != voiceSettingsState(a.cfg)
		a.settingsVoiceState = ""
		if target == screenConversation && voiceChanged && a.runtime != nil && a.runtime.ReloadVoice != nil {
			a.conversation.setStatus("Applying voice settings…", false)
			return a, reloadVoice(a.runCtx, a.runtime.ReloadVoice, resumeVoice)
		}
		if resumeVoice {
			return a, a.conversation.setInputMuted(false)
		}
		return a, nil

	case voiceReloadedMsg:
		if msg.err != nil {
			a.conversation.setStatus("Voice settings: "+msg.err.Error(), true)
		} else {
			a.conversation.setStatus("Voice settings applied", false)
		}
		if msg.resumeVoice {
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
			liveSpeaker:    msg.rt.LiveSpeaker,
			ctx:            a.runCtx,
			wg:             a.wg,
		})
		return a, cmd

	case quitMsg:
		if a.screen == screenPickBook {
			a.pickBook.cancelResolve()
		}
		if a.screen == screenLibrary {
			a.library.cancelPrepare()
		}
		a.remote.stop()
		a.quitting = true
		return a, tea.Quit
	}

	// Conversation owns background voice/text turns. Deliver their async
	// completions even when the user is on Settings (or another screen) so
	// turnState cannot wedge on turnVoiceCanceling forever after /settings.
	if a.screen != screenConversation {
		switch msg.(type) {
		case voiceTurnDoneMsg, textTurnDoneMsg, voiceRetryMsg, busEventMsg:
			var convCmd tea.Cmd
			a.conversation, convCmd = a.conversation.Update(msg)
			// Still deliver the message to the active screen below; batch cmds.
			var cmd tea.Cmd
			switch a.screen {
			case screenLauncher:
				a.launcher, cmd = a.launcher.Update(msg)
			case screenSettings:
				a.settings, cmd = a.settings.Update(msg)
			case screenSessions:
				a.sessions, cmd = a.sessions.Update(msg)
			case screenMeetingSetup:
				a.meetingSetup, cmd = a.meetingSetup.Update(msg)
			case screenMeeting:
				var m tea.Model
				m, cmd = a.meeting.Update(msg)
				a.meeting = m.(meetingModel)
			case screenMeetingResults:
				a.meetingResults, cmd = a.meetingResults.Update(msg)
			case screenMeetingRoute:
				a.meetingRoute, cmd = a.meetingRoute.Update(msg)
			case screenAudiobook:
				a.audiobook, cmd = a.audiobook.Update(msg)
			case screenPickBook:
				a.pickBook, cmd = a.pickBook.Update(msg)
			case screenLibrary:
				a.library, cmd = a.library.Update(msg)
			case screenRemote:
				a.remote, cmd = a.remote.Update(msg)
			case screenPersonas:
				a.personas, cmd = a.personas.Update(msg)
			}
			return a, tea.Batch(convCmd, cmd)
		}
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
	case screenMeetingResults:
		a.meetingResults, cmd = a.meetingResults.Update(msg)
	case screenMeetingRoute:
		a.meetingRoute, cmd = a.meetingRoute.Update(msg)
	case screenAudiobook:
		a.audiobook, cmd = a.audiobook.Update(msg)
	case screenPickBook:
		a.pickBook, cmd = a.pickBook.Update(msg)
	case screenLibrary:
		a.library, cmd = a.library.Update(msg)
	case screenRemote:
		a.remote, cmd = a.remote.Update(msg)
	case screenPersonas:
		a.personas, cmd = a.personas.Update(msg)
	}

	return a, cmd
}

func voiceSettingsState(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		cfg.TTSProvider, cfg.TTSFallbackProvider, cfg.TTSVoice,
		cfg.QwenTTSMode, cfg.QwenTTSVoice, cfg.QwenTTSLanguage)
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
	case screenMeetingResults:
		return a.meetingResults.View()
	case screenMeetingRoute:
		return a.meetingRoute.View()
	case screenAudiobook:
		return a.audiobook.View()
	case screenPickBook:
		return a.pickBook.View()
	case screenLibrary:
		return a.library.View()
	case screenRemote:
		return a.remote.View()
	case screenPersonas:
		return a.personas.View()
	default:
		return ""
	}
}
