package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
)

type testClipboard struct {
	value string
	err   error
}

func (c *testClipboard) ReadAll() (string, error) {
	return c.value, c.err
}

func (c *testClipboard) WriteAll(value string) error {
	c.value = value
	return c.err
}

func TestConversationUsesInjectedClipboardBackend(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clipboard := &testClipboard{value: "from clipboard"}
	m.deps.clipboard = clipboard
	m.input.SetValue("draft")
	m.moveCursorToOffset(len([]rune("draft")))
	m.syncEditorFromTextarea()

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("Ctrl+V did not request clipboard read")
	}
	m, _ = m.Update(cmd())
	if got := m.input.Value(); got != "draftfrom clipboard" {
		t.Fatalf("clipboard paste = %q, want draftfrom clipboard", got)
	}
}

func TestCopyLastAssistantViaKeysAndCommand(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clip := &testClipboard{}
	m.deps.clipboard = clip
	m.handleEvent(events.ResponseReady{Response: "Hello from Samantha."})
	if m.lastAssistantText != "Hello from Samantha." {
		t.Fatalf("lastAssistantText = %q", m.lastAssistantText)
	}

	// ctrl+y always yanks the last reply.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if clip.value != "Hello from Samantha." {
		t.Fatalf("ctrl+y clipboard = %q", clip.value)
	}

	// Bare y with empty composer also yanks.
	clip.value = ""
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if clip.value != "Hello from Samantha." {
		t.Fatalf("y clipboard = %q", clip.value)
	}

	// Typing y into a non-empty draft must not steal the key.
	clip.value = ""
	m.input.SetValue("yes")
	m.moveCursorToOffset(len([]rune("yes")))
	m.syncEditorFromTextarea()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if clip.value != "" {
		t.Fatalf("y while drafting must not copy, clipboard=%q", clip.value)
	}

	// /copy all includes user + assistant plain text.
	m.input.SetValue("")
	m.syncEditorFromTextarea()
	m.echoUserTurn("How are you?")
	m.handleEvent(events.ResponseReady{Response: "Doing well."})
	clip.value = ""
	m.runCopyCommand([]string{"all"})
	if !strings.Contains(clip.value, "You: How are you?") || !strings.Contains(clip.value, "Doing well.") {
		t.Fatalf("/copy all clipboard = %q", clip.value)
	}
}

func TestIdleCtrlCCopiesLastReplyInsteadOfQuitting(t *testing.T) {
	app := App{
		cfg:          &config.Config{},
		screen:       screenConversation,
		conversation: sizedConversation(t, 80, 24),
	}
	clip := &testClipboard{}
	app.conversation.deps.clipboard = clip
	app.conversation.lastAssistantText = "Copy me"
	app.conversation.input.SetValue("")
	app.conversation.syncEditorFromTextarea()

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if app.quitting {
		t.Fatal("idle ctrl+c with a reply must not quit")
	}
	if cmd != nil && cmdEmitsQuit(cmd) {
		t.Fatal("idle ctrl+c returned a quit command")
	}
	if clip.value != "Copy me" {
		t.Fatalf("clipboard = %q, want Copy me", clip.value)
	}

	// With a draft, ctrl+c still quits.
	app.conversation.input.SetValue("draft")
	app.conversation.syncEditorFromTextarea()
	app.quitting = false
	model, cmd = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if !app.quitting {
		t.Fatal("ctrl+c with a draft must still quit")
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func cmdEmitsQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, sub := range msg {
			if cmdEmitsQuit(sub) {
				return true
			}
		}
	}
	return false
}
