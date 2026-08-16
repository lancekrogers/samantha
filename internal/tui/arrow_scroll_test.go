package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestConversationArrowsScrollWhenComposerEmpty covers what makes native mouse
// selection affordable. Samantha no longer claims the mouse, so a terminal in
// the alternate screen turns wheel events into arrow keys; if arrows only ever
// moved the composer cursor, the wheel would stop scrolling the transcript.
//
// The rule is the one Home/End already use, so drafting is unaffected.
func TestConversationArrowsScrollWhenComposerEmpty(t *testing.T) {
	m := sizedConversation(t, 80, 10)
	for i := range 50 {
		m.appendTranscript(fmt.Sprintf("line %d", i))
	}
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: viewport should start at bottom")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.viewport.AtBottom() {
		t.Fatal("Up with an empty composer did not scroll the transcript")
	}
	scrolled := m.viewport.YOffset

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.viewport.YOffset <= scrolled {
		t.Fatalf("Down with an empty composer did not scroll back: YOffset %d -> %d", scrolled, m.viewport.YOffset)
	}
}

// TestConversationArrowsMoveCursorWhileDrafting is the other half: a draft in
// progress keeps the arrows, so multiline editing never needs a mode switch.
func TestConversationArrowsMoveCursorWhileDrafting(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	for i := range 50 {
		m.appendTranscript(fmt.Sprintf("line %d", i))
	}

	m.input.SetValue("first line\nsecond line")
	m.viewport.GotoBottom()
	offset := m.viewport.YOffset
	startLine := m.input.Line()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.viewport.YOffset != offset {
		t.Errorf("Up hijacked the transcript while drafting: YOffset %d -> %d", offset, m.viewport.YOffset)
	}
	if got := m.input.Value(); got != "first line\nsecond line" {
		t.Errorf("draft changed under an arrow key: %q", got)
	}
	// Not just "the viewport held still": the arrow has to actually reach the
	// composer. Asserting only stability would pass if the key were dropped.
	if got := m.input.Line(); got >= startLine {
		t.Errorf("Up did not move the composer cursor: line %d -> %d", startLine, got)
	}
}
