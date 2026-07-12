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
	defer f.mu.Unlock()
	f.textInputs = append(f.textInputs, input)
	return f.textErr
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

// Enter while the voice turn is already responding must not cancel it; the
// draft stays in the input box.
func TestSubmitWhileRespondingKeepsDraft(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	m.handleEvent(events.UserInput{Text: "spoken words"}) // listening -> responding

	if m.turnState != turnVoiceResponding {
		t.Fatalf("turnState = %d, want responding after UserInput event", m.turnState)
	}

	m, cmd := typeAndEnter(m, "my draft")
	if cmd != nil {
		t.Fatal("submit while responding must not dispatch")
	}
	if m.input.Value() != "my draft" {
		t.Errorf("draft lost: input = %q", m.input.Value())
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
