package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tui/anim"
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

func TestConversationStreamsResponseDeltasThenFinalizes(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.ready = true

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ResponseDelta{Text: "Hel"})
	m.handleEvent(events.ResponseDelta{Text: "lo"})

	// Mid-stream the partial reply must already be visible and live-buffered.
	if m.streamingAgent != "Hello" {
		t.Fatalf("streamingAgent = %q, want %q", m.streamingAgent, "Hello")
	}
	if !strings.Contains(m.View(), "Hello") {
		t.Fatalf("streamed text not visible mid-stream:\n%s", m.View())
	}

	m.handleEvent(events.ResponseReady{Response: "Hello, world."})

	// After finalize the live buffer is cleared and the canonical reply is in
	// the transcript exactly once.
	if m.streamingAgent != "" {
		t.Fatalf("streamingAgent = %q after ResponseReady, want empty", m.streamingAgent)
	}
	view := m.View()
	if !strings.Contains(view, "Hello, world.") {
		t.Fatalf("finalized reply missing from view:\n%s", view)
	}
	if strings.Count(view, "● Samantha") != 1 {
		t.Fatalf("agent turn rendered %d times, want 1:\n%s", strings.Count(view, "● Samantha"), view)
	}
}

func TestConversationClearsStaleStreamOnNewTurn(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.ready = true

	// A turn streams a delta but never finalizes (e.g. interrupted).
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ResponseDelta{Text: "orphaned partial"})

	// The next turn begins; the stale buffer must be discarded.
	m.handleEvent(events.ThinkingStarted{})
	if m.streamingAgent != "" {
		t.Fatalf("streamingAgent = %q at new turn, want empty", m.streamingAgent)
	}
	if strings.Contains(m.View(), "orphaned partial") {
		t.Fatalf("stale streamed text leaked into next turn:\n%s", m.View())
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

func TestVoiceMeterTracksInputLevelAndModes(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.STTPhase{Phase: "listening"})
	if m.status != "Listening" {
		t.Fatalf("status = %q, want Listening", m.status)
	}

	m.handleEvent(events.AudioLevel{Source: "input", Level: 0.8})
	if m.inputLevel < 0.7 {
		t.Fatalf("inputLevel = %v, want elevated after AudioLevel", m.inputLevel)
	}
	// Strong mic energy while listening promotes to the hearing animation.
	if m.status != "Hearing you" {
		t.Fatalf("status = %q after loud input, want Hearing you", m.status)
	}
	if !strings.Contains(m.View(), "Hearing you") {
		t.Fatalf("voice meter not visible in view:\n%s", m.View())
	}

	m, cmd := m.Update(voiceTickMsg(time.Now()))
	if m.voiceFrame != 1 {
		t.Fatalf("voiceFrame = %d after tick, want 1", m.voiceFrame)
	}
	if cmd == nil {
		t.Fatal("active voice mode must keep the animation tick armed")
	}

	m.handleEvent(events.SpeakingStarted{})
	if m.status != "Speaking" {
		t.Fatalf("status = %q, want Speaking", m.status)
	}
	m.handleEvent(events.SpeakingComplete{})
	if m.status != "" {
		t.Fatalf("status = %q after speaking complete, want empty", m.status)
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
	if m.followChat {
		t.Fatal("pgup must clear sticky chat follow")
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
	if !m.followChat {
		t.Fatal("ctrl+end must re-enable sticky chat follow")
	}

	m.appendTranscript("tail line")
	if !m.viewport.AtBottom() {
		t.Error("append at bottom must keep following the tail")
	}
}

// Voice-panel reflow shrinks the chat viewport. Without sticky follow, AtBottom
// flips false and every later message stays off-screen.
func TestConversationFollowSurvivesVoicePanelReflow(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	for i := range 40 {
		m.appendTranscript(fmt.Sprintf("history %d", i))
	}
	if !m.viewport.AtBottom() || !m.followChat {
		t.Fatal("precondition: following the chat tail")
	}

	// Thinking opens the voice strip and reflows chrome — the classic path that
	// used to strand YOffset above the new max after height shrinks.
	m.setVoiceMode(anim.ModeThinking)
	if !m.viewport.AtBottom() {
		t.Fatalf("reflow while following must keep chat pinned to bottom (YOffset=%d)", m.viewport.YOffset)
	}

	m.echoUserTurn("can you see this?")
	view := stripANSI(m.View())
	if !strings.Contains(view, "can you see this?") {
		t.Fatalf("user message missing after reflow:\n%s", view)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("echo after reflow must still pin to bottom")
	}
}

func TestEchoUserTurnDedupesMatchingUserInput(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.echoUserTurn("hello from keyboard")
	m.handleEvent(events.UserInput{Text: "hello from keyboard"})

	view := stripANSI(m.View())
	if got := strings.Count(view, "hello from keyboard"); got != 1 {
		t.Fatalf("user text rendered %d times, want 1 (optimistic + bus dedupe):\n%s", got, view)
	}
	if got := strings.Count(view, "› You"); got != 1 {
		t.Fatalf("user bubbles = %d, want 1:\n%s", got, view)
	}
	if m.pendingUserEcho != "" {
		t.Fatalf("pendingUserEcho = %q after matching UserInput, want empty", m.pendingUserEcho)
	}
}

func TestVoiceUserInputStillRendersWithoutEcho(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.UserInput{Text: "spoken aloud"})
	view := stripANSI(m.View())
	if !strings.Contains(view, "spoken aloud") {
		t.Fatalf("voice UserInput missing from chat:\n%s", view)
	}
}

func TestTimedOutMetricsDoNotSpamActivity(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.STTPhase{Phase: "listening"})
	m.handleEvent(events.TurnMetrics{Outcome: "timed_out"})
	m.handleEvent(events.STTPhase{Phase: "listening"})
	m.handleEvent(events.TurnMetrics{Outcome: "timed_out"})
	m.handleEvent(events.STTPhase{Phase: "listening"})

	var details []string
	for _, e := range m.activity {
		details = append(details, e.stage+":"+e.detail)
	}
	// One listening row; consecutive restarts and timed_out are suppressed.
	if got := strings.Count(strings.Join(details, ","), "input:listening"); got != 1 {
		t.Fatalf("listening activity rows = %v, want a single listening entry", details)
	}
	for _, d := range details {
		if strings.Contains(d, "timed_out") {
			t.Fatalf("timed_out must not spam activity: %v", details)
		}
	}
}

func TestEmptyResponseReadyLeavesVisibleTrace(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.ToolCallStarted{Name: "list_files", Summary: "."})
	m.handleEvent(events.ToolCallFinished{Name: "list_files", Preview: "a/"})
	m.handleEvent(events.ResponseReady{Response: ""})

	view := stripANSI(m.View())
	if !strings.Contains(view, "no reply") {
		t.Fatalf("empty non-interrupted ResponseReady must leave a chat trail:\n%s", view)
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
