package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/app"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

// brainSessionReporter adapts an optional brain.SessionReporter into the
// conversationDeps callback, so /session degrades gracefully on providers
// that do not expose session state.
func brainSessionReporter(p brain.Provider) func() (brain.SessionState, bool) {
	r, ok := p.(brain.SessionReporter)
	if !ok {
		return nil
	}
	return func() (brain.SessionState, bool) { return r.SessionInfo(), true }
}

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
	turnVoiceResponding           // voice turn past transcription — text Enter barges in (cancels TTS/brain)
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

type compactDoneMsg struct {
	err error
}

type voiceRetryMsg struct{}

// conversationDeps wires the live pipeline into the conversation model.
type conversationDeps struct {
	runner       turnRunner
	bus          *events.Bus
	clearHistory func()
	// compact runs the pipeline's conversation compaction; nil when the
	// runtime did not wire it (e.g. prompt resolution failed).
	compact func(ctx context.Context) error
	// brainSession reports the provider's live session state for /session;
	// nil (or ok=false) when the provider does not expose one.
	brainSession   func() (brain.SessionState, bool)
	voice          bool // STT is configured; voice turns may run
	output         bool // TTS/player are configured
	setOutputMuted func(bool)
	clipboard      clipboardBackend
	sessionID      string
	inputDevice    string
	outputDevice   string
	liveSpeaker    LiveSpeakerController
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
	m.liveSpeaker = deps.liveSpeaker
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
	if deps.liveSpeaker != nil {
		m.liveSpeakerStats = deps.liveSpeaker.Stats()
		m.liveSpeakerStatsKnown = true
		cmds = append(cmds, liveSpeakerStatsCmd(deps.liveSpeaker))
	}
	// Scripted meter for VHS/termcast demos — skips real mic/TTS turns.
	if demoVoiceAnimEnabled() {
		cmds = append(cmds, startDemoVoiceAnim(deps.bus), m.ensureVoiceTick())
		return tea.Batch(cmds...)
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
		// Stop the listening/hearing meter while input is paused.
		if m.voiceMode == anim.ModeListening || m.voiceMode == anim.ModeHearing || m.voiceMode == anim.ModeTranscribing {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}
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

// recoverTurnState unsticks cancel/response bookkeeping after screen changes
// drop async turn-done messages (e.g. /settings while a cancel was in flight).
func (m *conversationModel) recoverTurnState() tea.Cmd {
	switch m.turnState {
	case turnVoiceCanceling, turnVoiceResponding, turnTextRunning:
		// Drop orphaned cancel bookkeeping; a live pipeline turn may still be
		// finishing, but the TUI must accept input again. The next resume or
		// text submit is gated by turnIdle.
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		m.pendingText = ""
		m.pendingCompact = false
		m.turnState = turnIdle
		if m.canCancelVoice != nil {
			m.canCancelVoice.Store(false)
		}
	}
	return nil
}

// handleSubmit routes an Enter press by turn state: idle submits; any in-flight
// voice or text turn is canceled first so typed text can barge in repeatedly
// (not only the first voice reply).
//
// Slash commands are local (never need the brain). They run immediately in
// any turn state so a long STT cancel or in-flight response cannot freeze
// /help, /settings, or unknown-command feedback.
func (m *conversationModel) handleSubmit() tea.Cmd {
	if m.deps.runner == nil {
		return nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	text = expandPaletteSelection(text, m.commandSelection)

	// Local slash commands never require canceling the voice pipeline.
	if cmd, handled := m.submitLocalSlash(text); handled {
		return cmd
	}

	switch m.turnState {
	case turnIdle:
		previous := m.input.Value()
		m.input.Reset()
		m.editor.sync("", 0)
		m.editor.resetUndo()
		m.syncComposer(previous)
		return m.submitText(text)
	case turnVoiceListening:
		// Prefer the synchronous cancel gate over turnState: UserInput can
		// already have been emitted (brain thinking) while the bridge has
		// not yet delivered it into handleEvent. After the gate closes,
		// turnState becomes turnVoiceResponding on the next UserInput event
		// and text barge-in is handled there instead.
		if m.canCancelVoice != nil && !m.canCancelVoice.Load() {
			// Transcript already final; treat as responding barge-in if we
			// still hold a turn cancel (brain/TTS may be in flight).
			if m.turnCancel != nil {
				return m.beginTextBargeIn(text)
			}
			return nil
		}
		return m.beginTextBargeIn(text)
	case turnVoiceResponding, turnTextRunning:
		// "Speaking — type to barge in" applies to both voice and text turns:
		// the first barge-in runs as text, so the reply is turnTextRunning —
		// Enter must cancel that turn too or barge-in only works once.
		return m.beginTextBargeIn(text)
	default:
		// Already canceling a prior barge-in; leave the draft until drain.
		return nil
	}
}

// beginTextBargeIn parks the draft, echoes it, and cancels the in-flight voice
// or text turn. handleVoiceTurnDone / handleTextTurnDone dispatch the text when
// the cancel drains.
func (m *conversationModel) beginTextBargeIn(text string) tea.Cmd {
	m.pendingText = text
	previous := m.input.Value()
	m.input.Reset()
	m.editor.sync("", 0)
	m.editor.resetUndo()
	m.syncComposer(previous)
	// Show the bubble immediately — cancel + text dispatch can take a
	// full STT/TTS shutdown, and clearing the composer otherwise looks like
	// the message was dropped.
	if !isNonChatSubmit(text) {
		m.echoUserTurn(text)
	}
	m.turnState = turnVoiceCanceling
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(false)
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	// Stop audible playback ASAP when the pipeline exposes a player stop.
	if m.deps.runner != nil {
		if stopper, ok := m.deps.runner.(interface{ StopPlayback() }); ok {
			stopper.StopPlayback()
		}
	}
	return nil
}

// keystrokeBargeIn silences TTS the moment the user types a printable rune
// while a response is in flight — the behavior the composer label promises
// ("type to barge in"). Unlike the Enter path, only playback stops: the turn
// keeps streaming so the reply text still lands, and the keystroke proceeds
// into the composer as the start of the user's next message. vim-normal and
// vim-visual keys never reach here, so transcript navigation stays silent.
func (m *conversationModel) keystrokeBargeIn(msg tea.KeyMsg) {
	if msg.Type != tea.KeyRunes || msg.Alt {
		return
	}
	if m.turnState != turnVoiceResponding && m.turnState != turnTextRunning {
		return
	}
	if m.deps.runner == nil {
		return
	}
	if stopper, ok := m.deps.runner.(interface{ StopPlayback() }); ok {
		stopper.StopPlayback()
	}
}

// discardDraft drops a barged-in draft that /compact beat to the cancel drain.
// The echo bubble stays as scrollback, but the pending-echo marker has to clear:
// it suppresses the next matching UserInput bubble, so leaving it set would
// swallow the user's re-send of the same text. The drop is announced because the
// draft was echoed and is never going to be answered.
func (m *conversationModel) discardDraft(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if m.pendingUserEcho == text {
		m.pendingUserEcho = ""
	}
	m.emit(events.Info{Message: "Compacting — the message you typed was not sent. Send it again once the summary lands."})
}

// expandPaletteSelection replaces an incomplete slash prefix with the
// highlighted palette match (e.g. "/sett" → "/settings") so Enter runs the
// selected command instead of reporting "Unknown command".
func expandPaletteSelection(text string, selection int) string {
	matches := matchingSlashCommands(text)
	if len(matches) == 0 {
		return text
	}
	token := commandToken(text)
	if token == "" {
		return text
	}
	if _, exact := commandForToken(token); exact {
		return text
	}
	selected := matches[min(selection, len(matches)-1)]
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) <= 1 {
		return selected.name
	}
	return selected.name + " " + strings.Join(fields[1:], " ")
}

// submitLocalSlash handles slash-command lines without touching turn state.
// handled is true for any line that starts with "/" (including unknown names).
func (m *conversationModel) submitLocalSlash(text string) (cmd tea.Cmd, handled bool) {
	command, args, found, slash := parseSlashCommand(text)
	if !slash {
		return nil, false
	}
	previous := m.input.Value()
	m.input.Reset()
	m.editor.sync("", 0)
	m.editor.resetUndo()
	m.syncComposer(previous)

	if !found {
		token := commandToken(text)
		msg := "Unknown command " + token + ". Type /help to list commands."
		if suggestion := suggestSlashCommand(token); suggestion != "" {
			msg = "Unknown command " + token + ". Did you mean " + suggestion + "? Type /help to list commands."
		}
		m.commandError(msg)
		// Leave voice/response turns alone — do not cancel or redispatch.
		return nil, true
	}
	return m.executeSlashCommand(command, args), true
}

// isNonChatSubmit reports slash commands and built-in control phrases that
// must not leave a user bubble in the transcript.
func isNonChatSubmit(text string) bool {
	if _, _, _, slash := parseSlashCommand(text); slash {
		return true
	}
	cmd := app.NormalizeCommand(text)
	return app.IsExitCommand(cmd) || app.IsClearCommand(cmd)
}

// submitText applies the command policy to typed input — commands never reach
// the brain, matching app.Run's text loop — then dispatches a text turn.
// Slash commands are normally handled earlier by submitLocalSlash; this path
// remains for the cancel-drain pendingText route.
func (m *conversationModel) submitText(text string) tea.Cmd {
	if cmd, handled := m.submitLocalSlash(text); handled {
		// If a slash was queued behind a voice cancel, restore listening when
		// the command itself does not navigate away or redispatch.
		if cmd != nil {
			return cmd
		}
		return m.resumeListening()
	}

	cmd := app.NormalizeCommand(text)
	switch {
	case app.IsExitCommand(cmd):
		m.quitting = true
		return tea.Quit

	case app.IsClearCommand(cmd):
		if m.deps.clearHistory != nil {
			m.deps.clearHistory()
		}
		m.emit(events.ConversationCleared{})
		return m.resumeListening()

	}

	// Idle submit path: echo now. Cancel path already echoed in handleSubmit;
	// echoUserTurn is idempotent via pendingUserEcho on the later UserInput.
	if m.pendingUserEcho != text {
		m.echoUserTurn(text)
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
		if m.pendingCompact {
			m.pendingCompact = false
			m.discardDraft(text)
			return m.dispatchCompact()
		}
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
	// Barge-in during a text turn (agent speaking after a prior typed message)
	// sets turnVoiceCanceling + pendingText, then cancel drains here — same
	// path as voice barge-in via handleVoiceTurnDone.
	wasCanceling := m.turnState == turnVoiceCanceling
	pending := m.pendingText
	m.pendingText = ""
	m.turnState = turnIdle

	if wasCanceling {
		if m.pendingCompact {
			m.pendingCompact = false
			m.discardDraft(pending)
			return m.dispatchCompact()
		}
		if pending != "" {
			return m.submitText(pending)
		}
		return m.resumeListening()
	}

	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.emit(events.Error{Message: msg.err.Error()})
	}
	return m.resumeListening()
}

// requestCompact runs /compact when the pipeline is free, or cancels a
// cancelable in-flight voice turn and queues the compact for when the cancel
// drains. Compaction rebuilds the provider's context, so it never runs
// mid-turn; while a reply is in flight the user is asked to retry.
func (m *conversationModel) requestCompact() tea.Cmd {
	if m.deps.compact == nil {
		m.commandError("/compact is unavailable")
		return m.resumeListening()
	}
	switch m.turnState {
	case turnIdle:
		return m.dispatchCompact()
	case turnVoiceListening:
		if m.canCancelVoice != nil && m.canCancelVoice.Load() {
			m.pendingCompact = true
			m.turnState = turnVoiceCanceling
			m.canCancelVoice.Store(false)
			if m.turnCancel != nil {
				m.turnCancel()
			}
			return nil
		}
		m.commandError("busy — try /compact again after this turn")
		return nil
	case turnVoiceCanceling:
		m.pendingCompact = true
		return nil
	default: // turnVoiceResponding, turnTextRunning
		m.commandError("busy — try /compact again after this turn")
		return nil
	}
}

// dispatchCompact owns the pipeline for the compact operation the same way a
// text turn does, so a voice turn cannot start mid-compaction.
func (m *conversationModel) dispatchCompact() tea.Cmd {
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning
	m.setStatus("Compacting conversation", false)

	compact, wg := m.deps.compact, m.deps.wg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		defer cancel()
		if ctx.Err() != nil {
			return compactDoneMsg{err: ctx.Err()}
		}
		return compactDoneMsg{err: compact(ctx)}
	}
}

func (m *conversationModel) handleCompactDone(msg compactDoneMsg) tea.Cmd {
	m.turnCancel = nil
	m.turnState = turnIdle
	m.setStatus("", false)
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.emit(events.Error{Stage: "compact", Message: msg.err.Error()})
	}
	return m.resumeListening()
}

func (m *conversationModel) handleVoiceRetry() tea.Cmd {
	if m.turnState == turnIdle && m.voiceOn() {
		return m.dispatchVoiceTurn()
	}
	return nil
}
