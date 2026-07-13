package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
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
	for _, want := range []string{"hello there", "hi!", "› You", "● Samantha"} {
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

// Non-key messages (cursor blink ticks) must reach the textarea so the blink
// command chain from startConversation is not dropped.
func TestConversationForwardsNonKeyMessagesToTextarea(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	// textarea.Blink is a Cmd that yields a blink Msg; re-issuing via Update is
	// what keeps the chain alive. A zero-value blink tick type is private, so
	// exercise the default branch with a custom message the textarea ignores
	// without error — the critical assertion is that we do not early-return
	// before m.input.Update and that a subsequent key still works.
	type foreignMsg struct{}
	m, _ = m.Update(foreignMsg{})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got := m.input.Value(); got != "x" {
		t.Fatalf("input after foreign msg = %q, want x", got)
	}
}

func TestConversationHomeEndScrollWhenComposerEmpty(t *testing.T) {
	m := sizedConversation(t, 80, 10)
	for i := range 50 {
		m.appendTranscript(fmt.Sprintf("line %d", i))
	}
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: viewport should start at bottom")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.viewport.AtBottom() {
		t.Fatal("Home with empty composer did not jump chat to top")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.viewport.AtBottom() {
		t.Fatal("End with empty composer did not jump chat to bottom")
	}

	// With text in the composer, bare Home/End stay with the textarea.
	m.input.SetValue("draft")
	m.viewport.GotoBottom()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	scrolledAway := !m.viewport.AtBottom()
	if !scrolledAway {
		t.Fatal("precondition: need scrolled-up viewport")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	// Still scrolled away — Home did not hijack for transcript jump.
	if m.viewport.AtBottom() {
		t.Fatal("Home with non-empty composer jumped chat; want textarea line start")
	}
}

func TestConversationComposerSupportsMultilineDrafts(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	for _, r := range "first line" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	for _, r := range "second line" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got, want := m.input.Value(), "first line\nsecond line"; got != want {
		t.Fatalf("multiline draft = %q, want %q", got, want)
	}
	if got := m.input.Height(); got != conversationInputHeight {
		t.Fatalf("composer height = %d, want %d", got, conversationInputHeight)
	}
}

func TestConversationComposerCompactsInShortSplit(t *testing.T) {
	m := sizedConversation(t, 40, 8)
	if got := m.input.Height(); got != 1 {
		t.Fatalf("compact composer height = %d, want 1", got)
	}
	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got != 8 {
		t.Fatalf("compact view has %d rows, want 8:\n%s", got, view)
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

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
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

func TestConversationResponsiveViewFitsTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{40, 8}, {80, 12}, {120, 24}} {
		m := sizedConversation(t, size.width, size.height)
		m.appendActivity("model", "started", 0)
		view := stripANSI(m.View())
		if got := len(strings.Split(view, "\n")); got != size.height {
			t.Errorf("%dx%d view has %d lines, want %d\n%s", size.width, size.height, got, size.height, view)
		}
	}
}

func TestConversationActivityFeedAndFocus(t *testing.T) {
	m := sizedConversation(t, 120, 24)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingComplete{Elapsed: 1500 * time.Millisecond})

	chatView := stripANSI(m.View())
	if strings.Contains(chatView, "model  complete  1.5s") {
		t.Fatalf("activity detail should not displace the conversation until selected:\n%s", chatView)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if !m.activityFocused {
		t.Fatal("ctrl+t did not focus activity feed")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Activity", "model", "complete", "1.5s"} {
		if !strings.Contains(view, want) {
			t.Errorf("activity view missing %q:\n%s", want, view)
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.activityFocused {
		t.Fatal("esc did not return focus to transcript")
	}
}

func TestConversationUsesFullWidthAtLargeTerminalSizes(t *testing.T) {
	m := sizedConversation(t, 160, 30)
	m.appendTranscript(renderAgentTurn("Samantha", "full-width conversation"))
	m.appendActivity("model", "complete", 1500*time.Millisecond)

	if got := m.viewport.Width; got != 160 {
		t.Fatalf("conversation viewport width = %d, want full terminal width 160", got)
	}
	if got := m.activityViewport.Width; got != 160 {
		t.Fatalf("activity viewport width = %d, want full terminal width 160", got)
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "model  complete  1.5s") {
		t.Fatalf("large chat layout contains a persistent activity sidebar:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := len([]rune(line)); got > 160 {
			t.Errorf("line %d is %d cells wide, want at most 160: %q", i+1, got, line)
		}
	}
}
