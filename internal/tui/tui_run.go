package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

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
	app.meetingSweep = sweepMeetingRoutesCmd(cfg)
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

	// The mouse is left to the terminal by default (tui_mouse_enabled=false),
	// so click-drag selection and copy work the way they do in any other agent
	// chat. A transcript exists to be read and copied out of; taking selection
	// away to gain wheel routing is the wrong side of that trade.
	//
	// Nothing is lost to get it. Page Up / Page Down always scroll, and with no
	// mouse claim a terminal in the alternate screen sends arrow keys for wheel
	// turns, which the conversation scrolls with while the composer is empty.
	//
	// tui_mouse_enabled=true opts back in to cell-motion reporting (wheel and
	// trackpad delivered straight to the viewport, pointer movement reported
	// only while a button is held) at the cost of needing a modifier to select
	// — option in iTerm2, fn in Terminal.app, shift in kitty.
	//
	// The claim is not a startup option because only the conversation routes
	// wheel events — App.Update takes it on entering that screen and releases
	// it on leaving, so the launcher, settings and meeting screens are
	// unaffected either way.
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
