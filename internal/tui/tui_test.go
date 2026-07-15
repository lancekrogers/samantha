package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
)

// Ctrl+C must cancel an in-flight voice preview before quitting, otherwise an
// orphaned preview goroutine keeps playing while the pipeline starts its own
// audio player.
func TestCtrlCCancelsPreview(t *testing.T) {
	cancelled := false
	app := App{cfg: &config.Config{}}
	app.settings.previewCancel = func() { cancelled = true }
	app.tailscale.server = newTailscaleServer()

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
	if !app.tailscale.server.stopping.Load() {
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

func TestSwitchToTailscaleStartsManagedServerScreen(t *testing.T) {
	app := App{cfg: &config.Config{}, runCtx: context.Background()}
	model, cmd := app.Update(switchScreenMsg(screenTailscale))
	got := model.(App)
	if got.screen != screenTailscale {
		t.Fatalf("screen = %v, want Tailscale", got.screen)
	}
	if got.tailscale.server == nil || cmd == nil {
		t.Fatal("Tailscale screen did not prepare a managed server start")
	}
	// Do not execute cmd: that would recursively launch the test binary's
	// real serve command. The managed-process behavior has an injected test.
}
