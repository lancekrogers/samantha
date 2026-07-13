package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/app"
	"github.com/lancekrogers/samantha/internal/events"
)

// turnRunner is the slice of pipeline.Pipeline the conversation driver uses.
type turnRunner interface {
	RunTurn(ctx context.Context) (string, error)
	RunTurnTextMode(ctx context.Context, input string) error
}

// turnState tracks the single turn allowed in flight: pipeline turn methods
// assume one turn owns the pipeline's shared state at a time.
type turnState int

const (
	turnIdle            turnState = iota
	turnVoiceListening            // voice turn in flight, no final transcript yet — cancelable
	turnVoiceResponding           // voice turn past transcription — a submit must wait
	turnVoiceCanceling            // canceled for a text submit, awaiting voiceTurnDoneMsg
	turnTextRunning               // text turn in flight
)

type voiceTurnDoneMsg struct {
	text string
	err  error
}

type textTurnDoneMsg struct {
	err error
}

type voiceRetryMsg struct{}

// conversationDeps wires the live pipeline into the conversation model.
type conversationDeps struct {
	runner         turnRunner
	bus            *events.Bus
	clearHistory   func()
	voice          bool // STT is configured; voice turns may run
	output         bool // TTS/player are configured
	setOutputMuted func(bool)
	sessionID      string
	inputDevice    string
	outputDevice   string
	ctx            context.Context // pipeline lifetime; parent of every turn ctx
	wg             *sync.WaitGroup // tracks in-flight turns so shutdown can drain them
}

// startConversation attaches the model to a live pipeline and kicks off the
// first turn. The returned Cmd must be handed to the program.
func (m *conversationModel) startConversation(deps conversationDeps) tea.Cmd {
	m.deps = deps
	m.startedAt = time.Now()
	m.activity = nil
	m.voiceEnabled = deps.voice
	m.outputAvailable = deps.output
	m.sessionID = deps.sessionID
	m.inputDevice = deps.inputDevice
	m.outputDevice = deps.outputDevice
	m.bridge = newEventBridge(0)
	m.bridge.attach(deps.bus)

	// Mark the voice turn non-cancelable the moment the final transcript is
	// emitted — synchronously on the pipeline goroutine, before the bridge
	// drains UserInput into Update. Without this, Enter can still cancel
	// mid-think while turnState is still turnVoiceListening.
	if m.canCancelVoice == nil {
		m.canCancelVoice = &atomic.Bool{}
	}
	if deps.bus != nil {
		gate := m.canCancelVoice
		events.Subscribe(deps.bus, func(events.UserInput) {
			gate.Store(false)
		})
	}

	cmds := []tea.Cmd{m.bridge.wait(), textarea.Blink}
	m.appendActivity("session", shortSessionID(deps.sessionID), 0)
	if deps.voice {
		m.appendActivity("input", deviceLabel(deps.inputDevice), 0)
	}
	if deps.output {
		m.appendActivity("output", deviceLabel(deps.outputDevice), 0)
	}
	if m.voiceOn() {
		cmds = append(cmds, m.dispatchVoiceTurn())
	}
	return tea.Batch(cmds...)
}

func (m *conversationModel) voiceOn() bool {
	return m.deps.runner != nil && m.deps.voice && m.voiceEnabled
}

func (m *conversationModel) emit(e events.Event) {
	if m.deps.bus != nil {
		m.deps.bus.Emit(e)
	}
}

// toggleInputMuted flips voice-input pause for Ctrl+G. Absolute /mute and
// /unmute use setInputMuted so repeated commands do not invert state.
func (m *conversationModel) toggleInputMuted() tea.Cmd {
	return m.setInputMuted(m.voiceEnabled)
}

// setInputMuted pauses or resumes background voice turns. muted=true forces
// listening off; muted=false forces it on. Capture hardware may still be open
// while paused — only dispatch is gated. If the pipeline is only listening,
// muting cancels that turn immediately; a response that already owns the
// pipeline is allowed to finish and listening stays off.
func (m *conversationModel) setInputMuted(muted bool) tea.Cmd {
	if !m.deps.voice {
		m.setStatus("Microphone unavailable", true)
		return nil
	}
	if muted {
		if !m.voiceEnabled {
			return nil
		}
		m.voiceEnabled = false
		m.emit(events.Info{Message: "Voice input paused."})
		if m.turnState == turnVoiceListening && m.canCancelVoice != nil && m.canCancelVoice.Load() {
			m.turnState = turnVoiceCanceling
			if m.turnCancel != nil {
				m.turnCancel()
			}
		}
		return nil
	}
	if m.voiceEnabled {
		return nil
	}
	m.voiceEnabled = true
	m.voiceFailures = 0
	m.emit(events.Info{Message: "Voice input resumed."})
	return m.resumeListening()
}

func (m *conversationModel) toggleOutputMuted() {
	if !m.outputAvailable {
		m.setStatus("Voice output unavailable", true)
		return
	}
	m.outputMuted = !m.outputMuted
	if m.deps.setOutputMuted != nil {
		m.deps.setOutputMuted(m.outputMuted)
	}
	state := "unmuted"
	if m.outputMuted {
		state = "muted"
	}
	m.emit(events.Info{Message: "Voice output " + state + "."})
}

// dispatchVoiceTurn starts one voice turn under a per-turn cancel context
// owned by the model (D1): submitting text while this turn is listening
// cancels it.
func (m *conversationModel) dispatchVoiceTurn() tea.Cmd {
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnVoiceListening
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(true)
	}

	runner, wg := m.deps.runner, m.deps.wg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		defer cancel()
		// A Cmd can execute after shutdown began (cancel happens before the
		// runtime waits on wg); never enter the pipeline on a dead context.
		if ctx.Err() != nil {
			return voiceTurnDoneMsg{err: ctx.Err()}
		}
		text, err := runner.RunTurn(ctx)
		return voiceTurnDoneMsg{text: text, err: err}
	}
}

func (m *conversationModel) dispatchTextTurn(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning

	runner, wg := m.deps.runner, m.deps.wg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		defer cancel()
		if ctx.Err() != nil {
			return textTurnDoneMsg{err: ctx.Err()}
		}
		return textTurnDoneMsg{err: runner.RunTurnTextMode(ctx, text)}
	}
}

// resumeListening restarts the background voice turn when nothing else owns
// the pipeline.
func (m *conversationModel) resumeListening() tea.Cmd {
	if m.turnState == turnIdle && m.voiceOn() {
		return m.dispatchVoiceTurn()
	}
	return nil
}

// handleSubmit routes an Enter press by turn state: idle submits, a listening
// voice turn is canceled first (D1), anything else keeps the text in the box.
func (m *conversationModel) handleSubmit() tea.Cmd {
	if m.deps.runner == nil {
		return nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}

	switch m.turnState {
	case turnIdle:
		m.input.Reset()
		return m.submitText(text)
	case turnVoiceListening:
		// Prefer the synchronous cancel gate over turnState: UserInput can
		// already have been emitted (brain thinking) while the bridge has
		// not yet delivered it into handleEvent.
		if m.canCancelVoice != nil && !m.canCancelVoice.Load() {
			return nil
		}
		m.pendingText = text
		m.input.Reset()
		m.turnState = turnVoiceCanceling
		if m.turnCancel != nil {
			m.turnCancel()
		}
		return nil
	default:
		// A response or text turn owns the pipeline; leave the draft alone.
		return nil
	}
}

// submitText applies the command policy to typed input — commands never reach
// the brain, matching app.Run's text loop — then dispatches a text turn.
func (m *conversationModel) submitText(text string) tea.Cmd {
	cmd := app.NormalizeCommand(text)
	switch {
	case cmd == "/mute":
		return m.setInputMuted(true)
	case cmd == "/unmute":
		return m.setInputMuted(false)
	case cmd == "/mic":
		return m.toggleInputMuted()
	case cmd == "/audio" || cmd == "/speaker":
		m.toggleOutputMuted()
		return m.resumeListening()
	case cmd == "/activity" || cmd == "/timeline":
		m.activityFocused = !m.activityFocused
		return m.resumeListening()
	case app.IsExitCommand(cmd):
		m.quitting = true
		return tea.Quit

	case app.IsClearCommand(cmd):
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
		return m.resumeListening()

	case app.IsResumeVoiceCommand(cmd):
		if m.deps.voice && !m.voiceEnabled {
			m.voiceEnabled = true
			m.voiceFailures = 0
			m.emit(events.Info{Message: "Switching back to voice mode."})
		}
		return m.resumeListening()
	}

	return m.dispatchTextTurn(text)
}

func (m *conversationModel) handleVoiceTurnDone(msg voiceTurnDoneMsg) tea.Cmd {
	m.turnCancel = nil
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(false)
	}
	wasCanceling := m.turnState == turnVoiceCanceling
	m.turnState = turnIdle

	if wasCanceling {
		text := m.pendingText
		m.pendingText = ""
		if text != "" {
			return m.submitText(text)
		}
		return m.resumeListening()
	}

	if msg.err != nil {
		switch app.ClassifyVoiceFailure(msg.err, m.deps.ctx.Err(), m.voiceFailures+1) {
		case app.VoiceShutdown:
			m.quitting = true
			return tea.Quit
		case app.VoiceFallback:
			m.voiceFailures = 0
			m.voiceEnabled = false
			m.emit(events.Error{Message: msg.err.Error()})
			m.emit(events.Info{Message: "Voice input keeps failing — switching to text. Type /voice to switch back."})
			return nil
		default: // app.VoiceRetry
			m.voiceFailures++
			m.emit(events.Error{Message: msg.err.Error()})
			return tea.Tick(app.RetryBackoff, func(time.Time) tea.Msg { return voiceRetryMsg{} })
		}
	}

	m.voiceFailures = 0
	if msg.text == "" {
		return m.resumeListening() // silence — keep listening
	}

	// Voice commands match post-turn, identical to today: a spoken "goodbye"
	// has already received its spoken reply before the exit check runs.
	cmd := app.NormalizeCommand(msg.text)
	switch {
	case app.IsExitCommand(cmd):
		m.quitting = true
		return tea.Quit
	case app.IsClearCommand(cmd):
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
	}
	return m.resumeListening()
}

func (m *conversationModel) handleTextTurnDone(msg textTurnDoneMsg) tea.Cmd {
	m.turnCancel = nil
	m.turnState = turnIdle
	if msg.err != nil {
		m.emit(events.Error{Message: msg.err.Error()})
	}
	return m.resumeListening()
}

func (m *conversationModel) handleVoiceRetry() tea.Cmd {
	if m.turnState == turnIdle && m.voiceOn() {
		return m.dispatchVoiceTurn()
	}
	return nil
}
