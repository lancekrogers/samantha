package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sizedConversation(t *testing.T, width, height int) conversationModel {
	t.Helper()
	m := newConversation("Samantha")
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if !m.ready {
		t.Fatal("model not ready after WindowSizeMsg")
	}
	return m
}

func TestConversationViewBeforeResize(t *testing.T) {
	m := newConversation("Samantha")
	if m.View() == "" {
		t.Fatal("View must render a placeholder before the first WindowSizeMsg")
	}
}

func TestConversationAppendAndView(t *testing.T) {
	m := sizedConversation(t, 80, 24)

	m.appendTranscript(renderUserTurn("hello there"))
	m.appendTranscript(renderAgentTurn("Samantha", "hi!"))

	view := m.View()
	for _, want := range []string{"hello there", "hi!", "You:", "Samantha:"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q:\n%s", want, view)
		}
	}
}

func TestConversationClearTranscript(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.appendTranscript(renderUserTurn("wipe me"))
	m.clearTranscript()

	if strings.Contains(m.View(), "wipe me") {
		t.Error("clearTranscript left old content in the view")
	}
}

func TestConversationStatus(t *testing.T) {
	m := sizedConversation(t, 80, 24)

	m.setStatus("Listening...", false)
	if !strings.Contains(m.View(), "Listening...") {
		t.Error("status text not rendered")
	}

	m.setStatus("something broke", true)
	if !strings.Contains(m.View(), "something broke") {
		t.Error("error status not rendered")
	}
}

func TestConversationTypingGoesToInput(t *testing.T) {
	m := sizedConversation(t, 80, 24)

	for _, r := range "hi there" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != "hi there" {
		t.Errorf("input value = %q, want %q", got, "hi there")
	}
}

func TestConversationScrollAndFollow(t *testing.T) {
	m := sizedConversation(t, 80, 10)

	for i := range 50 {
		m.appendTranscript(fmt.Sprintf("line %d", i))
	}
	if !m.viewport.AtBottom() {
		t.Fatal("viewport should follow the tail while at bottom")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("pgup did not scroll away from the bottom")
	}

	// New content must not yank the view back down while reviewing history.
	m.appendTranscript("fresh line while scrolled up")
	if m.viewport.AtBottom() {
		t.Error("append while scrolled up must not auto-follow")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.viewport.AtBottom() {
		t.Error("end key did not jump to bottom")
	}

	m.appendTranscript("tail line")
	if !m.viewport.AtBottom() {
		t.Error("append at bottom must keep following the tail")
	}
}

func TestConversationResizeKeepsContent(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.appendTranscript(renderUserTurn("survives resize"))

	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if !strings.Contains(m.View(), "survives resize") {
		t.Error("content lost on resize")
	}
	if m.viewport.Width != 40 {
		t.Errorf("viewport width = %d, want 40", m.viewport.Width)
	}
}
