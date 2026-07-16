package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
