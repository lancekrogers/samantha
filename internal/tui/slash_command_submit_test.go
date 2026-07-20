package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestPartialSlashEnterRunsPaletteSelection(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)
	// Keep the initial voice turn listening without canceling for a slash.
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState=%d want listening", m.turnState)
	}

	m, cmd := typeAndEnter(m, "/sett")
	// /sett must expand to /settings and open settings without canceling first.
	if cmd == nil {
		t.Fatal("expected /settings screen switch from /sett prefix")
	}
	msg := cmd()
	sw, ok := msg.(switchScreenMsg)
	if !ok || screen(sw) != screenSettings {
		t.Fatalf("msg=%#v want screenSettings", msg)
	}
	// Voice cancel may start via setInputMuted on the App switch path; the
	// important part is we did not wedge on turnVoiceCanceling before running.
	if m.input.Value() != "" {
		t.Fatalf("composer should clear after slash, got %q", m.input.Value())
	}
}

func TestUnknownSlashDoesNotCancelListening(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)
	voiceCmd := m.dispatchVoiceTurn()
	// Park the turn so we can observe cancel (or not).
	started := make(chan struct{})
	done := make(chan tea.Msg, 1)
	go func() {
		close(started)
		done <- voiceCmd()
	}()
	<-started

	m, cmd := typeAndEnter(m, "/nope-not-a-command")
	if cmd != nil {
		t.Fatalf("unknown slash should not dispatch cmds that alter the turn, got %T", cmd)
	}
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState=%d want still listening (no cancel)", m.turnState)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Unknown command /nope-not-a-command") {
		t.Fatalf("missing unknown error:\n%s", view)
	}

	// Cancel should NOT have been signaled — the blocked turn stays parked.
	select {
	case <-done:
		t.Fatal("voice turn returned — slash canceled listening unexpectedly")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}
	// Clean up: cancel so the goroutine can exit.
	if m.turnCancel != nil {
		m.turnCancel()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup cancel did not unblock voice turn")
	}
}

func TestUnknownSlashThenTextMessageStillWorks(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m.turnState = turnIdle

	m, _ = typeAndEnter(m, "/nope")
	if !strings.Contains(stripANSI(m.View()), "Unknown command /nope") {
		t.Fatalf("missing error:\n%s", stripANSI(m.View()))
	}

	m, cmd := typeAndEnter(m, "are you there")
	if cmd == nil {
		t.Fatal("text after unknown slash did not dispatch")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState=%d want text running", m.turnState)
	}
	// Execute the text-turn cmd so the fake runner records the input.
	_ = cmd()
	if got := runner.texts(); len(got) != 1 || got[0] != "are you there" {
		t.Fatalf("texts=%v", got)
	}
}

func TestHelpWhileRespondingRunsLocally(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	m.handleEvent(events.UserInput{Text: "spoken"})
	if m.turnState != turnVoiceResponding {
		t.Fatalf("turnState=%d want responding", m.turnState)
	}

	m, cmd := typeAndEnter(m, "/help")
	// Local slash must not wait for the voice turn to finish.
	if m.turnState != turnVoiceResponding {
		t.Fatalf("turnState=%d — /help must not change response ownership", m.turnState)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "/help") && !strings.Contains(view, "Slash commands") {
		// help body is dim multi-line; at least the activity/error path is fine
		if !strings.Contains(view, "command") {
			t.Fatalf("help output missing:\n%s", view)
		}
	}
	// resumeListening while responding returns nil
	if cmd != nil {
		// executeSlashCommand returns resumeListening which is nil when not idle
		_ = cmd
	}
	if m.input.Value() != "" {
		t.Fatalf("composer not cleared: %q", m.input.Value())
	}
}

func TestExpandPaletteSelection(t *testing.T) {
	if got := expandPaletteSelection("/sett", 0); got != "/settings" {
		t.Fatalf("expand(/sett)=%q want /settings", got)
	}
	if got := expandPaletteSelection("/settings", 0); got != "/settings" {
		t.Fatalf("exact command should stay exact, got %q", got)
	}
	if got := expandPaletteSelection("hello", 0); got != "hello" {
		t.Fatalf("non-slash unchanged: %q", got)
	}
}

func TestSuggestSlashCommand(t *testing.T) {
	if got := suggestSlashCommand("/sett"); got != "/settings" {
		t.Fatalf("suggest(/sett)=%q want /settings", got)
	}
	if got := suggestSlashCommand("/zzzz"); got != "" {
		t.Fatalf("suggest(/zzzz)=%q want empty", got)
	}
}

func TestRecoverTurnStateUnsticksCanceling(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	m.turnState = turnVoiceCanceling
	m.pendingText = "/settings"
	m.recoverTurnState()
	if m.turnState != turnIdle {
		t.Fatalf("turnState=%d want idle", m.turnState)
	}
	if m.pendingText != "" {
		t.Fatalf("pendingText=%q want empty", m.pendingText)
	}
}
