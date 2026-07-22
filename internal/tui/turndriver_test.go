package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
)

// fakeTurnRunner scripts RunTurn results in order; a blockNext entry parks
// the turn until its context is canceled, like a silent listening session.
type fakeTurnRunner struct {
	mu         sync.Mutex
	voiceQueue []voiceScript
	voiceCalls int
	textInputs []string
	textErr    error
	blockText  bool // park RunTurnTextMode until ctx cancel (simulates TTS)
	stopped    int  // StopPlayback call count
}

type voiceScript struct {
	text  string
	err   error
	block bool
}

func (f *fakeTurnRunner) RunTurn(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.voiceCalls++
	var s voiceScript
	if len(f.voiceQueue) > 0 {
		s = f.voiceQueue[0]
		f.voiceQueue = f.voiceQueue[1:]
	}
	f.mu.Unlock()

	if s.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return s.text, s.err
}

func (f *fakeTurnRunner) RunTurnTextMode(ctx context.Context, input string) error {
	f.mu.Lock()
	f.textInputs = append(f.textInputs, input)
	block := f.blockText
	err := f.textErr
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeTurnRunner) StopPlayback() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeTurnRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.voiceCalls
}

func (f *fakeTurnRunner) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.textInputs...)
}

func startedConversation(t *testing.T, runner *fakeTurnRunner, voice bool) (conversationModel, *events.Bus) {
	t.Helper()
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner: runner,
		bus:    bus,
		voice:  voice,
		ctx:    context.Background(),
	})
	return m, bus
}

func typeAndEnter(m conversationModel, text string) (conversationModel, tea.Cmd) {
	m.input.SetValue(text)
	return m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestVoiceTurnCompletesAndRedispatches(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{text: "hello"}}}
	m, _ := startedConversation(t, runner, true)

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening after start", m.turnState)
	}

	m, cmd := m.Update(voiceTurnDoneMsg{text: "hello"})
	if cmd == nil {
		t.Fatal("completed voice turn did not redispatch")
	}
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening after redispatch", m.turnState)
	}
}

func TestSilenceKeepsListening(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	m, cmd := m.Update(voiceTurnDoneMsg{text: ""})
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatal("silent turn must keep listening")
	}
}

// D1: submitting text while a voice turn is listening cancels the voice turn,
// then dispatches the text turn once the canceled turn drains.
func TestTextSubmitCancelsListeningVoiceTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)

	voiceCmd := m.dispatchVoiceTurn() // execute the in-flight turn ourselves
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	m, cmd := typeAndEnter(m, "typed instead")
	if cmd != nil {
		t.Fatal("submit during listening must wait for the canceled turn, not dispatch")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling", m.turnState)
	}
	if m.input.Value() != "" {
		t.Error("input not cleared on submit")
	}
	// Composer is empty immediately — the bubble must already be in Chat so
	// the user does not think Enter dropped the message during cancel.
	view := stripANSI(m.View())
	if !strings.Contains(view, "typed instead") {
		t.Fatalf("typed message missing from chat during cancel wait:\n%s", view)
	}
	if m.activityFocused {
		t.Fatal("submit must switch focus back to Chat")
	}

	var msg tea.Msg
	select {
	case msg = <-voiceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled voice turn never returned — cancel not propagated")
	}

	m, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("drained cancel did not dispatch the pending text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}

	if textMsg := cmd(); textMsg == nil {
		t.Fatal("text turn cmd returned nil msg")
	}
	if got := runner.texts(); len(got) != 1 || got[0] != "typed instead" {
		t.Fatalf("RunTurnTextMode inputs = %v, want [typed instead]", got)
	}
	// Bus UserInput after dispatch must not double the optimistic bubble.
	m.handleEvent(events.UserInput{Text: "typed instead"})
	if got := strings.Count(stripANSI(m.View()), "typed instead"); got != 1 {
		t.Fatalf("user text rendered %d times after UserInput, want 1", got)
	}
}

func TestIdleTextSubmitEchoesImmediately(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m.turnState = turnIdle

	m, cmd := typeAndEnter(m, "hello chat")
	if cmd == nil {
		t.Fatal("idle submit must dispatch a text turn")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "hello chat") {
		t.Fatalf("idle typed message missing from chat before pipeline events:\n%s", view)
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}
}

// After a text turn finishes, the background voice turn resumes.
func TestVoiceResumesAfterTextTurn(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	m.turnState = turnTextRunning

	m, cmd := m.Update(textTurnDoneMsg{})
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatal("voice listening did not resume after text turn")
	}
}

func TestRuntimeMuteControls(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	outputMuted := false
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner: runner, bus: bus, voice: true, output: true,
		setOutputMuted: func(muted bool) { outputMuted = muted },
		ctx:            context.Background(),
	})

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.voiceEnabled || m.voiceOn() {
		t.Fatal("ctrl+g did not mute microphone input")
	}
	m.turnState = turnIdle
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !m.voiceEnabled || cmd == nil {
		t.Fatal("second ctrl+g did not resume microphone input")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.outputMuted || !outputMuted {
		t.Fatal("ctrl+o did not mute pipeline output")
	}
}

func TestAbsoluteMuteUnmuteCommands(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	// startConversation dispatches a listening turn; park in idle so absolute
	// unmute can redispatch cleanly after force-mute.
	m.turnState = turnIdle
	m.voiceEnabled = true

	m, cmd := typeAndEnter(m, "/mute")
	if m.voiceEnabled {
		t.Fatal("/mute did not force voice input off")
	}
	if cmd != nil {
		t.Fatal("/mute while idle must not dispatch a turn")
	}

	// Already muted: absolute /mute must stay muted (not toggle back on).
	m, cmd = typeAndEnter(m, "/mute")
	if m.voiceEnabled || cmd != nil {
		t.Fatal("muted → /mute must stay muted without redispach")
	}

	m, cmd = typeAndEnter(m, "/unmute")
	if !m.voiceEnabled || cmd == nil {
		t.Fatal("/unmute did not force voice input on and resume listening")
	}
	// Drain the resume cmd so the fake runner is not left mid-turn.
	_ = cmd()

	// Already unmuted: absolute /unmute must stay unmuted (not toggle off).
	before := runner.calls()
	m.turnState = turnIdle
	m, cmd = typeAndEnter(m, "/unmute")
	if !m.voiceEnabled {
		t.Fatal("unmuted → /unmute must stay unmuted")
	}
	if cmd != nil {
		_ = cmd()
	}
	if runner.calls() != before {
		t.Fatal("unmuted → /unmute must not redispatch another listening turn")
	}

	// /mic remains a toggle (Ctrl+G alias).
	m.turnState = turnIdle
	m, _ = typeAndEnter(m, "/mic")
	if m.voiceEnabled {
		t.Fatal("/mic did not toggle voice input off")
	}
}

// Muting while STT is still listening cancels the in-flight turn and does not
// redispatch voice listening while input stays paused. Ctrl+G mutes immediately;
// a typed /mute is parked as pending text and applied after the cancel drains.
func TestMuteWhileListeningCancelsWithoutRedispatch(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)

	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening", m.turnState)
	}

	// Immediate mute (Ctrl+G): cancel listening now; no redispatch while off.
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.voiceEnabled {
		t.Fatal("ctrl+g did not pause voice input")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after mute while listening", m.turnState)
	}
	if cmd != nil {
		t.Fatal("mute while listening must wait for cancel drain, not dispatch")
	}

	var msg tea.Msg
	select {
	case msg = <-voiceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("listening turn was not canceled by mute")
	}

	m, cmd = m.Update(msg)
	if m.voiceEnabled {
		t.Fatal("voice re-enabled after cancel drain")
	}
	if m.turnState != turnIdle {
		t.Fatalf("turnState = %d after cancel drain, want idle", m.turnState)
	}
	if cmd != nil {
		t.Fatal("canceled listening mute must not redispatch while voice input is off")
	}
}

// Enter while the voice turn is responding (speaking/thinking) barges in:
// cancel the voice turn and park the draft as pending text.
func TestSubmitWhileRespondingBargesIn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)
	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	m.handleEvent(events.UserInput{Text: "spoken words"}) // listening -> responding
	if m.turnState != turnVoiceResponding {
		t.Fatalf("turnState = %d, want responding after UserInput event", m.turnState)
	}

	m, cmd := typeAndEnter(m, "my draft")
	if cmd != nil {
		t.Fatal("barge-in submit should wait for voiceTurnDone, not dispatch immediately")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after text barge-in", m.turnState)
	}
	if m.pendingText != "my draft" {
		t.Fatalf("pendingText = %q, want my draft", m.pendingText)
	}
	if m.input.Value() != "" {
		t.Errorf("composer should clear after barge-in: %q", m.input.Value())
	}

	// Drain the canceled voice turn; handleVoiceTurnDone should dispatch text.
	select {
	case msg := <-voiceDone:
		m, cmd = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("voice turn was not canceled by barge-in")
	}
	if m.pendingText != "" {
		t.Fatalf("pendingText still set after drain: %q", m.pendingText)
	}
	if cmd == nil && m.turnState != turnTextRunning && m.turnState != turnIdle {
		// dispatchTextTurn returns a Cmd; if fake runner is sync it may complete.
		t.Logf("after barge drain: state=%d cmd=%v", m.turnState, cmd != nil)
	}
}

// After the first typed barge-in, the agent reply is a text turn with TTS.
// Enter during that reply must cancel again — otherwise barge-in only works once.
func TestSubmitWhileTextTurnBargesInAgain(t *testing.T) {
	runner := &fakeTurnRunner{blockText: true}
	m, _ := startedConversation(t, runner, true)
	m.turnState = turnIdle

	// First typed message (idle path) starts a long text turn (speaking).
	m, textCmd := typeAndEnter(m, "first barge")
	if textCmd == nil {
		t.Fatal("idle submit must dispatch text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}
	textDone := make(chan tea.Msg, 1)
	go func() { textDone <- textCmd() }()

	// Second Enter while the agent is still on the text turn.
	m, cmd := typeAndEnter(m, "second barge")
	if cmd != nil {
		t.Fatal("barge during text turn should wait for cancel drain")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after second barge", m.turnState)
	}
	if m.pendingText != "second barge" {
		t.Fatalf("pendingText = %q, want second barge", m.pendingText)
	}
	if runner.stopped < 1 {
		t.Fatal("StopPlayback should run on barge-in so TTS stops immediately")
	}

	select {
	case msg := <-textDone:
		m, cmd = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("text turn was not canceled by second barge-in")
	}
	if m.pendingText != "" {
		t.Fatalf("pendingText still set after drain: %q", m.pendingText)
	}
	if cmd == nil {
		t.Fatal("cancel drain must dispatch the second text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running for second barge", m.turnState)
	}
	// Run the newly dispatched text turn (non-blocking path: unblock runner).
	runner.mu.Lock()
	runner.blockText = false
	runner.mu.Unlock()
	if msg := cmd(); msg == nil {
		t.Fatal("second text turn cmd returned nil")
	}
	got := runner.texts()
	if len(got) < 2 || got[len(got)-1] != "second barge" {
		t.Fatalf("text inputs = %v, want … second barge", got)
	}
}

// UserInput can clear the cancel gate on the pipeline goroutine before the
// bridge delivers the event into Update — turnState may still be listening.
// Text barge-in must still cancel (same as responding) when turnCancel is set.
func TestSubmitAfterTranscriptGateBargesIn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, bus := startedConversation(t, runner, true)
	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening", m.turnState)
	}
	if !m.canCancelVoice.Load() {
		t.Fatal("canCancelVoice should be true while listening")
	}

	// Synchronous bus emit flips the gate without going through handleEvent.
	bus.Emit(events.UserInput{Text: "spoken already in brain history"})
	if m.canCancelVoice.Load() {
		t.Fatal("canCancelVoice still true after UserInput emit")
	}
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want still listening (bridge not drained)", m.turnState)
	}

	m, cmd := typeAndEnter(m, "my draft")
	if cmd != nil {
		t.Fatal("barge-in should wait for cancel drain")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after barge-in past transcript gate", m.turnState)
	}
	if m.pendingText != "my draft" {
		t.Fatalf("pendingText = %q", m.pendingText)
	}
	if m.input.Value() != "" {
		t.Errorf("composer should clear: %q", m.input.Value())
	}

	select {
	case msg := <-voiceDone:
		m, _ = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("voice turn was not canceled")
	}
}

func TestSpokenExitPhraseQuits(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	m, cmd := m.Update(voiceTurnDoneMsg{text: "Goodbye."})
	if !m.quitting {
		t.Fatal("spoken exit phrase did not mark quitting")
	}
	if cmd == nil {
		t.Fatal("no quit cmd returned")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("exit did not produce tea.QuitMsg")
	}
}

func TestTypedExitQuitsWithoutTurn(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false) // text-only: idle state
	m, cmd := typeAndEnter(m, "/q")

	if !m.quitting || cmd == nil {
		t.Fatal("typed /q did not quit")
	}
	if len(runner.texts()) != 0 {
		t.Error("exit command must not reach the brain")
	}
}

func TestClearCommandClearsHistoryNotBrainTurn(t *testing.T) {
	cleared := false
	bus := events.NewBus()
	runner := &fakeTurnRunner{}
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner:       runner,
		bus:          bus,
		clearHistory: func() { cleared = true },
		voice:        false,
		ctx:          context.Background(),
	})

	m, _ = typeAndEnter(m, "/clear")
	if !cleared {
		t.Fatal("clearHistory not called")
	}
	if len(runner.texts()) != 0 {
		t.Error("clear command must not reach the brain")
	}
}

func TestVoiceFailureFallsBackAfterThreshold(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	failure := errors.New("mic exploded")

	// Two retries, then fallback on the third consecutive failure.
	for i := range 2 {
		var cmd tea.Cmd
		m, cmd = m.Update(voiceTurnDoneMsg{err: failure})
		if cmd == nil {
			t.Fatalf("failure %d: no retry tick scheduled", i+1)
		}
		if !m.voiceEnabled {
			t.Fatalf("failure %d: fell back too early", i+1)
		}
		m.turnState = turnVoiceListening // simulate the retried dispatch
	}

	m, _ = m.Update(voiceTurnDoneMsg{err: failure})
	if m.voiceEnabled {
		t.Fatal("three consecutive failures did not fall back to text")
	}

	// /voice re-enables and dispatches a listening turn.
	before := runner.calls()
	m, cmd := typeAndEnter(m, "/voice")
	if !m.voiceEnabled {
		t.Fatal("/voice did not re-enable voice")
	}
	if cmd == nil {
		t.Fatal("/voice did not resume listening")
	}
	cmd()
	if runner.calls() != before+1 {
		t.Fatal("resumed listening did not call RunTurn")
	}
}

func TestVoiceErrorSurfacesOnBus(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var errMsgs []string
	events.Subscribe(bus, func(e events.Error) { errMsgs = append(errMsgs, e.Message) })

	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})

	m.Update(voiceTurnDoneMsg{err: errors.New("transient stt hiccup")})
	if len(errMsgs) != 1 || !strings.Contains(errMsgs[0], "transient stt hiccup") {
		t.Fatalf("voice failure not emitted as events.Error: %v", errMsgs)
	}
}

func TestShutdownClassificationQuits(t *testing.T) {
	runner := &fakeTurnRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: ctx})

	cancel() // parent ctx gone: the program is shutting down
	m, cmd := m.Update(voiceTurnDoneMsg{err: context.Canceled})
	if !m.quitting || cmd == nil {
		t.Fatal("canceled parent ctx did not quit the conversation")
	}
}

func TestTurnWaitGroupTracksInFlightTurns(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	// voice=false so startConversation doesn't dispatch a turn this test
	// never executes; the dispatch under test happens explicitly below.
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: false, ctx: ctx, wg: &wg})
	m.deps.voice = true
	m.voiceEnabled = true

	cmd := m.dispatchVoiceTurn()
	done := make(chan struct{})
	go func() { cmd(); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight turn did not drain on parent cancel")
	}
	wg.Wait() // must not deadlock: Done ran when the turn drained
}
