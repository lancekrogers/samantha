package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// Ctrl+C must cancel an in-flight voice preview before quitting, otherwise an
// orphaned preview goroutine keeps playing while the pipeline starts its own
// audio player.
func TestCtrlCCancelsPreview(t *testing.T) {
	cancelled := false
	app := App{cfg: &config.Config{}}
	app.settings.previewCancel = func() { cancelled = true }
	app.remote.server = newRemoteServer()

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Fatal("ctrl+c did not cancel in-flight preview")
	}
	if cmd == nil {
		t.Fatal("ctrl+c did not return a quit command")
	}
	if !model.(App).quitting {
		t.Fatal("ctrl+c did not mark app as quitting")
	}
	if !app.remote.server.stopping.Load() {
		t.Fatal("ctrl+c did not stop the managed Tailscale server")
	}
}

// Switching to the settings screen replaces the settings model; any in-flight
// preview must be cancelled first or its cancel func is orphaned.
func TestSwitchToSettingsCancelsPreview(t *testing.T) {
	cancelled := false
	app := App{cfg: &config.Config{}}
	app.settings.previewCancel = func() { cancelled = true }

	model, _ := app.Update(switchScreenMsg(screenSettings))
	if !cancelled {
		t.Fatal("screen switch did not cancel in-flight preview")
	}
	if model.(App).settings.previewCancel != nil {
		t.Fatal("settings model was not replaced on screen switch")
	}
}

// Opening settings recreates the model; geometry must be copied from the app
// because Bubble Tea does not re-emit WindowSize on screen switches.
func TestSwitchToSettingsAppliesTerminalGeometry(t *testing.T) {
	app := App{
		cfg:    &config.Config{},
		width:  120,
		height: 40,
	}
	// Prior window-size updates may have left stale geometry on the old model.
	app.settings.width, app.settings.height = 40, 12

	model, _ := app.Update(switchScreenMsg(screenSettings))
	got := model.(App).settings
	if got.width != 120 || got.height != 40 {
		t.Fatalf("settings geometry = %dx%d, want 120x40 from app", got.width, got.height)
	}
	// chrome is 5 rows in non-compact mode → 40 - 5 = 35 list rows
	if rows := got.visibleRows(); rows != 35 {
		t.Fatalf("visible rows = %d, want 35 for a 40-row terminal", rows)
	}
}

func TestSettingsReturnsToConversationAndRestoresVoice(t *testing.T) {
	app := App{cfg: &config.Config{}, screen: screenConversation}
	app.conversation = newConversation("Samantha")
	app.conversation.deps.voice = true
	app.conversation.voiceEnabled = true
	app.conversation.input.SetValue("draft to preserve")

	model, _ := app.Update(switchScreenMsg(screenSettings))
	app = model.(App)
	if app.screen != screenSettings {
		t.Fatalf("screen = %d, want settings", app.screen)
	}
	if app.settingsReturnScreen != screenConversation {
		t.Fatalf("settings return screen = %d, want conversation", app.settingsReturnScreen)
	}
	if app.conversation.voiceEnabled {
		t.Fatal("voice input remained enabled while settings was open")
	}

	model, _ = app.Update(settingsDoneMsg{})
	app = model.(App)
	if app.screen != screenConversation {
		t.Fatalf("screen after settings = %d, want conversation", app.screen)
	}
	if !app.conversation.voiceEnabled {
		t.Fatal("voice input was not restored after settings")
	}
	if got := app.conversation.input.Value(); got != "draft to preserve" {
		t.Fatalf("composer draft = %q, want preserved draft", got)
	}
}

func TestSettingsReloadsLiveVoiceBeforeResumingInput(t *testing.T) {
	reloaded := false
	app := App{cfg: &config.Config{}, screen: screenConversation, runCtx: context.Background()}
	app.runtime = &ConversationRuntime{ReloadVoice: func(context.Context) error {
		reloaded = true
		return nil
	}}
	app.conversation = newConversation("Samantha")
	app.conversation.deps.voice = true
	app.conversation.voiceEnabled = true

	model, _ := app.Update(switchScreenMsg(screenSettings))
	app = model.(App)
	app.cfg.TTSVoice = "af_bella"
	model, cmd := app.Update(settingsDoneMsg{})
	app = model.(App)
	if cmd == nil {
		t.Fatal("returning from Settings did not schedule live voice reload")
	}
	if app.conversation.voiceEnabled {
		t.Fatal("voice input resumed before provider reload completed")
	}

	model, _ = app.Update(cmd())
	app = model.(App)
	if !reloaded {
		t.Fatal("live voice reload was not invoked")
	}
	if !app.conversation.voiceEnabled {
		t.Fatal("voice input was not restored after provider reload")
	}
}

func TestSwitchToTailscaleStartsManagedServerScreen(t *testing.T) {
	app := App{cfg: &config.Config{}, runCtx: context.Background()}
	model, cmd := app.Update(switchScreenMsg(screenRemote))
	got := model.(App)
	if got.screen != screenRemote {
		t.Fatalf("screen = %v, want Tailscale", got.screen)
	}
	if got.remote.server == nil || cmd == nil {
		t.Fatal("Tailscale screen did not prepare a managed server start")
	}
	// Do not execute cmd: that would recursively launch the test binary's
	// real serve command. The managed-process behavior has an injected test.
}

func TestSwitchToPickBookStartsLibraryBrowse(t *testing.T) {
	app := App{cfg: &config.Config{CalibreEnabled: true}}
	model, cmd := app.Update(switchScreenMsg(screenPickBook))
	got := model.(App)
	if got.screen != screenPickBook {
		t.Fatalf("screen = %v, want pick book", got.screen)
	}
	if got.pickBook.focus != pickFocusList {
		t.Fatalf("pick-book focus = %v, want list", got.pickBook.focus)
	}
	if cmd == nil {
		t.Fatal("switching to pick book should start a browse")
	}
}

// The mouse claim costs native text selection, so only the conversation — the
// one screen that routes wheel events — should hold it. Leaving must give
// selection back.
func TestMouseClaimFollowsConversationScreen(t *testing.T) {
	app := App{cfg: &config.Config{TUIMouseEnabled: true}, screen: screenLauncher}
	claim, release := tea.EnableMouseCellMotion(), tea.DisableMouse()

	model, cmd := app.Update(switchScreenMsg(screenConversation))
	app = model.(App)
	if app.screen != screenConversation {
		t.Fatalf("screen = %v, want conversation", app.screen)
	}
	if !cmdEmits(cmd, claim) {
		t.Fatal("entering conversation did not claim the mouse")
	}

	// Staying put must not re-issue the claim.
	if _, cmd := app.Update(switchScreenMsg(screenConversation)); cmdEmits(cmd, claim) {
		t.Fatal("re-entering the same screen re-issued the mouse claim")
	}

	model, cmd = app.Update(switchScreenMsg(screenLauncher))
	if got := model.(App).screen; got != screenLauncher {
		t.Fatalf("screen = %v, want launcher", got)
	}
	if !cmdEmits(cmd, release) {
		t.Fatal("leaving conversation did not release the mouse")
	}
}

// tui_mouse_enabled=false is the opt-out for users who copy from the
// transcript: the claim must never be issued.
func TestMouseClaimSuppressedWhenDisabled(t *testing.T) {
	app := App{cfg: &config.Config{TUIMouseEnabled: false}, screen: screenLauncher}

	_, cmd := app.Update(switchScreenMsg(screenConversation))
	if cmdEmits(cmd, tea.EnableMouseCellMotion()) {
		t.Fatal("tui_mouse_enabled=false still claimed the mouse")
	}
}

// cmdEmits reports whether cmd produces want, unwrapping the BatchMsg that
// tea.Batch returns. Bubble Tea's mouse messages are unexported empty structs,
// so identity is by value.
func cmdEmits(cmd tea.Cmd, want tea.Msg) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			if cmdEmits(sub, want) {
				return true
			}
		}
		return false
	default:
		return msg == want
	}
}
