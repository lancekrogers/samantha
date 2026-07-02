package tui

import (
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
