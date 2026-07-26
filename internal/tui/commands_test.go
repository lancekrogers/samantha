package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

func TestSlashCommandRegistryMatchesCanonicalNamesAndAliases(t *testing.T) {
	tests := []struct {
		input string
		want  slashCommandID
	}{
		{input: "/help", want: commandHelp},
		{input: "/?", want: commandHelp},
		{input: "/speaker", want: commandAudio},
		{input: "/timeline", want: commandActivity},
		{input: "/settings", want: commandSettings},
		{input: "/q", want: commandQuit},
		{input: "/vim on", want: commandVim},
	}
	for _, tt := range tests {
		command, _, found, slash := parseSlashCommand(tt.input)
		if !slash || !found || command.id != tt.want {
			t.Errorf("parseSlashCommand(%q) = id %d, found %v, slash %v; want id %d", tt.input, command.id, found, slash, tt.want)
		}
	}
}

func TestSettingsCommandRequestsSettingsScreen(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	_, cmd := typeAndEnter(m, "/settings")

	if cmd == nil {
		t.Fatal("/settings did not request a screen change")
	}
	msg, ok := cmd().(switchScreenMsg)
	if !ok || screen(msg) != screenSettings {
		t.Fatalf("/settings message = %#v, want screenSettings", msg)
	}
	if len(runner.texts()) != 0 {
		t.Fatal("/settings reached the brain")
	}
}

func TestSlashCommandPaletteShowsEverythingThatFits(t *testing.T) {
	m := sizedConversation(t, 100, 30)
	m.input.SetValue("/")
	m.syncComposer("")

	view := stripANSI(m.View())
	for _, command := range slashCommands {
		if !strings.Contains(view, command.usage) {
			t.Errorf("large command palette missing %q:\n%s", command.usage, view)
		}
	}
	if got := len(strings.Split(view, "\n")); got != 30 {
		t.Fatalf("command palette view has %d rows, want 30:\n%s", got, view)
	}
}

func TestSlashCommandPaletteDoesNotBreakCompactComposer(t *testing.T) {
	m := sizedConversation(t, 40, 8)
	m.input.SetValue("/")
	m.syncComposer("")

	view := stripANSI(m.View())
	if strings.Contains(view, slashCommands[0].description) {
		t.Fatalf("compact composer rendered a palette with no room:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got != 8 {
		t.Fatalf("compact command view has %d rows, want 8:\n%s", got, view)
	}
}

func TestSlashCommandSelectionScrollsThroughAvailableRows(t *testing.T) {
	m := sizedConversation(t, 80, 16) // four palette rows fit
	m.input.SetValue("/")
	m.syncComposer("")
	for range 7 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	view := stripANSI(m.View())
	selected := slashCommands[7]
	if !strings.Contains(view, selected.usage) {
		t.Fatalf("selected command was not scrolled into palette:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got != 16 {
		t.Fatalf("limited palette view has %d rows, want 16:\n%s", got, view)
	}
}

func TestSlashCommandTabCompletesSelectedMatch(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.input.SetValue("/vi")
	m.syncComposer("")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); got != "/vim " {
		t.Fatalf("completed command = %q, want %q", got, "/vim ")
	}
}

func TestUnknownSlashCommandNeverReachesBrain(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m, cmd := typeAndEnter(m, "/definitely-not-real")

	if cmd != nil {
		t.Fatal("unknown slash command dispatched a turn")
	}
	if len(runner.texts()) != 0 {
		t.Fatalf("unknown slash command reached brain: %v", runner.texts())
	}
	if !strings.Contains(stripANSI(m.View()), "Unknown command /definitely-not-real") {
		t.Fatalf("unknown command error not visible:\n%s", stripANSI(m.View()))
	}
}

func TestHelpCommandUsesRegistry(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m, _ = typeAndEnter(m, "/help")

	view := stripANSI(m.View())
	for _, command := range slashCommands {
		if !strings.Contains(view, command.usage) {
			t.Errorf("/help output missing %q:\n%s", command.usage, view)
		}
	}
	if len(runner.texts()) != 0 {
		t.Fatal("/help reached brain")
	}
}

func TestSessionCommandReportsStateWithoutReachingBrain(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, bus := startedConversation(t, runner, false)
	m.deps.brainSession = func() (brain.SessionState, bool) {
		return brain.SessionState{Kind: "harness", ID: "sess-12345678", PromptTokens: 12340, Turns: 8}, true
	}

	var infos []string
	bus.SubscribeAll(func(e events.Event) {
		if i, ok := e.(events.Info); ok {
			infos = append(infos, i.Message)
		}
	})

	_, _ = typeAndEnter(m, "/session")

	if len(runner.texts()) != 0 {
		t.Fatal("/session reached the brain")
	}
	if len(infos) != 1 {
		t.Fatalf("infos = %v, want exactly one /session line", infos)
	}
	for _, wantPart := range []string{"harness", "sess-123", "12340 tok", "8 turns"} {
		if !strings.Contains(infos[0], wantPart) {
			t.Fatalf("info = %q, missing %q", infos[0], wantPart)
		}
	}
}

func TestSessionCommandDegradesWhenUnsupported(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, bus := startedConversation(t, runner, false)

	var infos []string
	bus.SubscribeAll(func(e events.Event) {
		if i, ok := e.(events.Info); ok {
			infos = append(infos, i.Message)
		}
	})

	_, _ = typeAndEnter(m, "/session")

	if len(infos) != 1 || !strings.Contains(infos[0], "unavailable") {
		t.Fatalf("infos = %v, want one unavailable line", infos)
	}
}

func TestSessionSummaryBranches(t *testing.T) {
	tests := []struct {
		name  string
		state brain.SessionState
		want  string
	}{
		{
			name:  "harness with live session and usage",
			state: brain.SessionState{Kind: brain.SessionKindHarness, ID: "sess-12345678", PromptTokens: 12340, Turns: 8},
			want:  "session: harness · id sess-123 · last prompt 12340 tok · 8 turns kept",
		},
		{
			name:  "harness fresh before first turn",
			state: brain.SessionState{Kind: brain.SessionKindHarness},
			want:  "session: harness · no CLI session yet · 0 turns kept",
		},
		{
			name:  "harness with turns but resume not wired",
			state: brain.SessionState{Kind: brain.SessionKindHarness, Turns: 12},
			want:  "session: harness · no live CLI session · 12 turns kept",
		},
		{
			name:  "chat kind estimates next request",
			state: brain.SessionState{Kind: brain.SessionKindChat, PromptTokens: 900, Turns: 5},
			want:  "session: local chat · est next prompt 900 tok · 5 turns kept",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newConversation("Samantha")
			state := tt.state
			m.deps.brainSession = func() (brain.SessionState, bool) { return state, true }
			if got := m.sessionSummary(); got != tt.want {
				t.Fatalf("sessionSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}
