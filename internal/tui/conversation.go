package tui

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

const conversationInputHeight = 3

// voiceTickInterval drives the conversation meter animation (~10 fps).
const voiceTickInterval = 100 * time.Millisecond

// voicePanelRows is the vertical space reserved for the compact voice EQ strip
// under the header (not a tall art panel).
const voicePanelRows = 5

// voiceTickMsg advances ambient voice animations.
type voiceTickMsg time.Time

// conversationModel renders the live conversation screen: a scrollable
// transcript viewport, a persistent status indicator, and an always-focused
// input line. It renders purely from injected state — turn dispatch and event
// bus wiring are layered on by later slices.
type conversationModel struct {
	agentName string
	cfg       *config.Config

	width  int
	height int
	ready  bool

	viewport         viewport.Model
	activityViewport viewport.Model
	input            textarea.Model

	transcript []string
	activity   []activityEntry
	status     string
	statusErr  bool
	// streamingAgent accumulates ResponseDelta text for the in-progress agent
	// turn, rendered live beneath the transcript until ResponseReady finalizes
	// it into transcript. Empty when no turn is streaming.
	streamingAgent string
	// pendingUserEcho is the last optimistically rendered user turn text. When
	// the pipeline later emits matching UserInput, the transcript skip avoids
	// a duplicate bubble (typed submit shows immediately; voice still waits
	// for the bus event).
	pendingUserEcho string

	bridge      *eventBridge
	lastMetrics events.TurnMetrics

	deps       conversationDeps
	turnState  turnState
	turnCancel func()
	// canCancelVoice is true only while STT is still listening (before the
	// final transcript). Updated synchronously from the bus handler on the
	// pipeline goroutine so Enter cannot race the async bridge drain of
	// UserInput into turnVoiceResponding. Pointer so Bubble Tea model copies
	// share one gate with the bus subscription.
	canCancelVoice  *atomic.Bool
	pendingText     string
	voiceEnabled    bool
	outputMuted     bool
	outputAvailable bool
	activityFocused bool
	// followChat / followActivity track whether the user is pinned to the
	// tail. bubbles/viewport AtBottom() alone is not enough: reflow (voice
	// panel open/close) shrinks height without adjusting YOffset, which
	// falsely reports "scrolled up" and freezes auto-scroll for the rest of
	// the session.
	followChat            bool
	followActivity        bool
	startedAt             time.Time
	sessionID             string
	inputDevice           string
	outputDevice          string
	voiceFailures         int
	quitting              bool
	liveSpeaker           LiveSpeakerController
	liveSpeakerStats      speaker.LiveStats
	liveSpeakerStatsKnown bool

	commandQuery     string
	commandSelection int
	editor           editorBuffer
	vim              vimState

	// Voice meter animation (festival-style multi-frame art + level bar).
	voiceMode     anim.Mode
	voiceFrame    int
	inputLevel    float64 // smoothed mic energy 0..1
	outputLevel   float64 // reserved; speaking uses synthetic envelope for now
	reducedMotion bool
	voiceTicking  bool
}

func newConversation(agentName string) conversationModel {
	if agentName == "" {
		agentName = "Samantha"
	}

	input := textarea.New()
	input.Placeholder = "Type a message or / for commands…"
	input.CharLimit = 1000
	input.ShowLineNumbers = false
	input.KeyMap.InsertNewline.SetKeys("ctrl+j", "ctrl+enter", "alt+enter", "shift+enter")
	input.KeyMap.InsertNewline.SetHelp("ctrl+j", "new line")
	input.SetHeight(conversationInputHeight)
	input.Focus()

	return conversationModel{
		agentName:      agentName,
		input:          input,
		canCancelVoice: &atomic.Bool{},
		followChat:     true,
		followActivity: true,
		startedAt:      time.Now(),
		reducedMotion:  anim.ReducedMotion(),
	}
}

func voiceTickCmd() tea.Cmd {
	return tea.Tick(voiceTickInterval, func(t time.Time) tea.Msg { return voiceTickMsg(t) })
}

type activityEntry struct {
	at      time.Duration
	stage   string
	detail  string
	elapsed time.Duration
}

func (m conversationModel) Update(msg tea.Msg) (conversationModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, m.ensureVoiceTick()

	case voiceTickMsg:
		m.voiceFrame++
		// Decay mic energy so the meter settles when speech pauses.
		m.inputLevel *= 0.82
		if m.inputLevel < 0.02 {
			m.inputLevel = 0
		}
		m.outputLevel *= 0.88
		if m.outputLevel < 0.02 {
			m.outputLevel = 0
		}
		if !m.shouldAnimateVoice() {
			m.voiceTicking = false
			return m, nil
		}
		return m, voiceTickCmd()

	case demoVoiceAnimStartedMsg:
		return m, m.ensureVoiceTick()

	case busEventMsg:
		m.handleEvent(msg.event)
		return m, tea.Batch(m.rearm(), m.ensureVoiceTick())

	case voiceTurnDoneMsg:
		return m, tea.Batch(m.handleVoiceTurnDone(msg), m.ensureVoiceTick())

	case textTurnDoneMsg:
		return m, tea.Batch(m.handleTextTurnDone(msg), m.ensureVoiceTick())

	case voiceRetryMsg:
		return m, tea.Batch(m.handleVoiceRetry(), m.ensureVoiceTick())

	case liveSpeakerStatsMsg:
		m.liveSpeakerStats = msg.stats
		m.liveSpeakerStatsKnown = true
		return m, liveSpeakerStatsCmd(m.liveSpeaker)

	case clipboardPasteMsg:
		if msg.err != nil {
			m.commandError("paste failed: " + msg.err.Error())
			return m, nil
		}
		m.insertClipboardText(msg.text)
		return m, nil

	case tea.KeyMsg:
		m.syncEditorFromTextarea()
		// Page keys scroll history. Editing keys stay with the always-focused
		// composer so multiline drafting never needs a mode switch.
		switch msg.String() {
		case "ctrl+v", "ctrl+shift+v", "shift+insert":
			return m, readClipboard(m.clipboard())
		case "ctrl+a":
			m.selectAll()
			return m, nil
		case "ctrl+x":
			if m.editor.selectionActive() {
				m.cutSelection()
				return m, nil
			}
		case "ctrl+g":
			return m, m.toggleInputMuted()
		case "ctrl+o":
			m.toggleOutputMuted()
			return m, nil
		case "ctrl+t":
			m.activityFocused = !m.activityFocused
			return m, nil
		case "esc":
			if m.vim.mode == vimVisual {
				m.enterVimNormal()
				return m, nil
			}
			if m.editor.selectionActive() {
				m.editor.clearSelection()
				return m, nil
			}
			if m.activityFocused {
				m.activityFocused = false
				return m, nil
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			return m.updateScroll(msg)
		case "ctrl+home":
			m.activeViewport().GotoTop()
			m.syncFollowFromViewports()
			return m, nil
		case "ctrl+end":
			m.activeViewport().GotoBottom()
			m.syncFollowFromViewports()
			return m, nil
		case "home":
			// Activity always jumps; Chat only when the composer is empty so
			// bare Home/End still navigate the transcript without fighting
			// line-start/end editing while drafting.
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoTop()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "end":
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoBottom()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "up", "down":
			if m.activityFocused {
				return m.updateScroll(msg)
			}
		}

		if handled, cmd := m.handleCommandPaletteKey(msg); handled {
			return m, cmd
		}
		if msg.String() == "enter" {
			return m, m.handleSubmit()
		}
		if m.vim.enabled {
			switch m.vim.mode {
			case vimNormal:
				return m, m.handleVimNormalKey(msg)
			case vimVisual:
				return m, m.handleVimVisualKey(msg)
			case vimInsert:
				if msg.String() == "esc" {
					m.enterVimNormal()
					return m, nil
				}
			}
		}
		return m, m.updateComposer(msg)

	default:
		// Non-key messages (notably textarea.Blink ticks) must reach the
		// composer so the cursor blink chain stays alive.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height
	m.reflow()
}

func (m *conversationModel) reflow() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Capture follow intent before height changes invalidate AtBottom().
	m.syncFollowFromViewports()
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Header + rule + label + input border + footer consume six rows in
	// addition to the textarea's own height. An active voice panel adds a few
	// more. Command matches consume only the rows currently available above
	// the composer.
	chrome := 6
	if m.voiceMode != anim.ModeIdle && m.height >= 14 {
		chrome += voicePanelRows
	}
	vpHeight := max(m.height-inputHeight-chrome-m.commandPaletteRows(), 1)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpHeight)
		m.activityViewport = viewport.New(max(m.width, 1), vpHeight)
		m.ready = true
		m.followChat = true
		m.followActivity = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpHeight
		m.activityViewport.Width = max(m.width, 1)
		m.activityViewport.Height = vpHeight
	}
	m.input.SetWidth(max(m.width-4, 1))
	m.input.SetHeight(inputHeight)
	m.refreshContent()
	m.refreshActivity()
	m.applyFollow()
}

// syncFollowFromViewports updates the sticky follow flags from the current
// scroll position. Call before mutating content or viewport height so a
// reflow cannot flip "at bottom" into a permanent freeze.
func (m *conversationModel) syncFollowFromViewports() {
	if !m.ready {
		m.followChat = true
		m.followActivity = true
		return
	}
	m.followChat = m.viewport.AtBottom()
	m.followActivity = m.activityViewport.AtBottom()
}

// applyFollow pins each pane to the tail when its follow flag is set.
func (m *conversationModel) applyFollow() {
	if !m.ready {
		return
	}
	if m.followChat {
		m.viewport.GotoBottom()
	}
	if m.followActivity {
		m.activityViewport.GotoBottom()
	}
}

func (m *conversationModel) activeViewport() *viewport.Model {
	if m.activityFocused {
		return &m.activityViewport
	}
	return &m.viewport
}

func (m conversationModel) updateScroll(msg tea.Msg) (conversationModel, tea.Cmd) {
	var cmd tea.Cmd
	if m.activityFocused {
		m.activityViewport, cmd = m.activityViewport.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	// Manual scroll owns follow: pgup freezes the tail; jumping to bottom
	// (End / ctrl+end) re-enables auto-follow for new messages.
	m.syncFollowFromViewports()
	return m, cmd
}

// appendTranscript adds rendered lines to the transcript, following the tail
// when followChat is set. Sticky flags (not live AtBottom) own follow intent
// so a prior reflow cannot freeze the chat mid-session.
func (m *conversationModel) appendTranscript(lines ...string) {
	m.transcript = append(m.transcript, lines...)
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) clearTranscript() {
	m.transcript = nil
	m.pendingUserEcho = ""
	m.refreshContent()
	if m.followChat {
		m.viewport.GotoBottom()
	}
}

// echoUserTurn renders the user's message into Chat immediately. Typed
// submits call this so clearing the composer never looks like the message
// vanished while a voice turn cancels or the brain is reached. Matching
// UserInput events skip a second bubble via pendingUserEcho.
func (m *conversationModel) echoUserTurn(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.activityFocused = false
	m.followChat = true
	m.pendingUserEcho = text
	m.appendTranscript(renderUserTurn(text))
}

func (m *conversationModel) setStatus(text string, isErr bool) {
	m.status = text
	m.statusErr = isErr
}

// setVoiceMode updates the animated meter state and reflows when the panel
// appears or disappears so the viewport height stays correct.
func (m *conversationModel) setVoiceMode(mode anim.Mode) {
	prev := m.voiceMode
	m.voiceMode = mode
	if mode == anim.ModeIdle {
		m.inputLevel = 0
	}
	if (prev == anim.ModeIdle) != (mode == anim.ModeIdle) && m.ready {
		m.reflow()
	}
}

func (m *conversationModel) shouldAnimateVoice() bool {
	if m.reducedMotion {
		return false
	}
	return m.voiceMode != anim.ModeIdle
}

// ensureVoiceTick starts the animation loop once while a voice mode is active.
func (m *conversationModel) ensureVoiceTick() tea.Cmd {
	if !m.shouldAnimateVoice() || m.voiceTicking {
		return nil
	}
	m.voiceTicking = true
	return voiceTickCmd()
}

func (m conversationModel) animStyles() anim.Styles {
	return voiceAnimStyles()
}

func (m conversationModel) voiceLevel() float64 {
	switch m.voiceMode {
	case anim.ModeHearing, anim.ModeListening, anim.ModeTranscribing:
		return m.inputLevel
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		if m.outputLevel > 0 {
			return m.outputLevel
		}
		return 0
	default:
		return 0
	}
}

func (m conversationModel) inputBorderColor() lipgloss.Color {
	switch m.voiceMode {
	case anim.ModeHearing:
		return colorHearing
	case anim.ModeListening:
		return colorAccent
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		return colorSpeak
	case anim.ModeError:
		return colorError
	case anim.ModeThinking, anim.ModeTranscribing:
		return colorStatus
	default:
		return colorAccent
	}
}

func (m *conversationModel) refreshContent() {
	if !m.ready {
		return
	}
	content := strings.Join(m.transcript, "\n")
	// Render the in-progress agent turn live beneath the finalized transcript
	// so the reply streams in token-by-token before ResponseReady lands.
	if m.streamingAgent != "" {
		live := renderAgentTurn(m.agentName, m.streamingAgent)
		if content != "" {
			content += "\n" + live
		} else {
			content = live
		}
	}
	// lipgloss wraps to width so long turns don't overflow the viewport.
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

// appendStreamingDelta grows the in-progress agent turn and re-renders,
// following the tail when followChat is set.
func (m *conversationModel) appendStreamingDelta(text string) {
	m.streamingAgent += text
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) appendActivity(stage, detail string, elapsed time.Duration) {
	m.activity = append(m.activity, activityEntry{
		at: time.Since(m.startedAt), stage: stage, detail: detail, elapsed: elapsed,
	})
	if len(m.activity) > 500 {
		m.activity = append([]activityEntry(nil), m.activity[len(m.activity)-500:]...)
	}
	m.refreshActivity()
	m.applyFollow()
}

func (m *conversationModel) refreshActivity() {
	if !m.ready {
		return
	}
	var lines []string
	for _, entry := range m.activity {
		when := fmt.Sprintf("%6.1fs", entry.at.Seconds())
		line := when + "  " + entry.stage
		if entry.detail != "" {
			line += "  " + entry.detail
		}
		if entry.elapsed > 0 {
			line += "  " + formatSeconds(entry.elapsed)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")
	m.activityViewport.SetContent(lipgloss.NewStyle().Width(max(m.activityViewport.Width, 1)).Render(content))
}

// seedTranscript pre-populates the viewport from persisted session turns.
// Roles are the normalized on-disk scheme ("user"/"assistant"); anything
// else (e.g. "tool") is dropped, matching brain.LoadHistory. Rendering goes
// through the same functions live events use so the two paths cannot drift.
func (m *conversationModel) seedTranscript(turns []brain.Turn) {
	for _, t := range turns {
		switch t.Role {
		case "user":
			m.appendTranscript(renderUserTurn(t.Content))
		case "assistant":
			m.appendTranscript(renderAgentTurn(m.agentName, t.Content), "")
		}
	}
}

// rearm re-issues the bridge drain Cmd; it must follow every consumed
// busEventMsg or the model stops receiving bus events.
func (m conversationModel) rearm() tea.Cmd {
	if m.bridge == nil {
		return nil
	}
	return m.bridge.wait()
}

// handleEvent maps one bus event onto viewport/status state. The mapping
// mirrors internal/ui's stdout renderer, minus the per-stage timing noise.
func (m *conversationModel) handleEvent(e events.Event) {
	// A final transcript means the brain now owns this turn: a text submit
	// past this point would kill a response in flight, so Enter waits.
	if _, ok := e.(events.UserInput); ok && m.turnState == turnVoiceListening {
		m.turnState = turnVoiceResponding
	}

	switch e := e.(type) {
	case events.AudioLevel:
		// Peak-hold + light smoothing so quiet frames don't flatten the meter.
		level := e.Level
		if level < 0 {
			level = 0
		}
		if level > 1 {
			level = 1
		}
		switch e.Source {
		case "output":
			if level > m.outputLevel {
				m.outputLevel = level
			} else {
				m.outputLevel = m.outputLevel*0.6 + level*0.4
			}
		default:
			if level > m.inputLevel {
				m.inputLevel = level
			} else {
				m.inputLevel = m.inputLevel*0.55 + level*0.45
			}
			// Real energy while listening implies the user is speaking.
			if m.voiceMode == anim.ModeListening && m.inputLevel > 0.12 {
				m.setVoiceMode(anim.ModeHearing)
				m.setStatus("Hearing you", false)
			}
		}

	case events.STTPhase:
		// Skip consecutive "listening" ticks from the no-speech restart loop.
		if e.Phase == "listening" && len(m.activity) > 0 {
			last := m.activity[len(m.activity)-1]
			if last.stage == "input" && last.detail == "listening" {
				m.setVoiceMode(anim.ModeListening)
				m.setStatus("Listening", false)
				break
			}
		}
		m.appendActivity("input", e.Phase, e.Elapsed)
		switch e.Phase {
		case "listening":
			m.setVoiceMode(anim.ModeListening)
			m.setStatus("Listening", false)
		case "hearing":
			m.setVoiceMode(anim.ModeHearing)
			m.setStatus("Hearing you", false)
		case "transcribing":
			m.setVoiceMode(anim.ModeTranscribing)
			m.setStatus("Transcribing", false)
		}

	case events.TranscriptPartial:
		if m.voiceMode != anim.ModeHearing && m.voiceMode != anim.ModeTranscribing {
			m.setVoiceMode(anim.ModeHearing)
		}
		m.setStatus(e.Text, false)

	case events.UserInput:
		m.appendActivity("input", "final", 0)
		// Typed submits already echoed the bubble; voice transcripts still need
		// one. Dedupe on exact text so a delayed cancel path cannot double-post.
		if m.pendingUserEcho != "" && m.pendingUserEcho == e.Text {
			m.pendingUserEcho = ""
			break
		}
		m.pendingUserEcho = ""
		m.activityFocused = false
		m.followChat = true
		m.appendTranscript(renderUserTurn(e.Text))

	case events.ThinkingStarted:
		// Start a fresh streaming buffer; a prior turn's leftover (e.g. after an
		// interrupt that skipped ResponseReady) must not bleed into this reply.
		// Re-render so any stale live partial leaves the viewport immediately.
		if m.streamingAgent != "" {
			m.streamingAgent = ""
			m.refreshContent()
			m.applyFollow()
		}
		m.appendActivity("model", "started", 0)
		m.setVoiceMode(anim.ModeThinking)
		m.setStatus(m.agentName+" thinking", false)

	case events.ResponseStreamingStarted:
		m.appendActivity("model", "first response", e.Elapsed)

	case events.ResponseDelta:
		m.appendStreamingDelta(e.Text)

	case events.ThinkingComplete:
		m.appendActivity("model", "complete", e.Elapsed)

	case events.SpeechSegmentReady:
		m.appendActivity("voice", "segment ready", 0)

	case events.GeneratingVoice:
		m.appendActivity("voice", "synthesizing", 0)
		m.setVoiceMode(anim.ModeSynthesizing)
		m.setStatus("Synthesizing voice", false)

	case events.VoiceGenerated:
		m.appendActivity("voice", "generated", e.Elapsed)

	case events.SpeakingStarted:
		m.appendActivity("output", "playing", 0)
		m.setVoiceMode(anim.ModeSpeaking)
		m.setStatus("Speaking", false)

	case events.SpeakingComplete:
		m.appendActivity("output", "complete", e.Elapsed)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("", false)

	case events.SpeakingInterrupted:
		m.appendActivity("output", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("speech interrupted ("+e.Reason+")", false)

	case events.TurnInterrupted:
		m.appendActivity("turn", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("turn interrupted ("+e.Reason+")", false)

	case events.ResponseReady:
		m.appendActivity("turn", "response ready", 0)
		// Text-only / no-TTS turns never emit SpeakingComplete; clear the
		// thinking status here so it does not stick after a successful reply.
		if m.voiceMode != anim.ModeSpeaking && m.voiceMode != anim.ModeSynthesizing {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}
		// Finalize the streamed turn: clear the live buffer first so it is not
		// rendered twice, then append the canonical response to the transcript.
		m.streamingAgent = ""
		if e.Response != "" {
			m.appendTranscript(renderAgentTurn(m.agentName, e.Response), "")
		} else if e.Interrupted {
			m.refreshContent()
			m.applyFollow()
		} else {
			// Tool-only or empty model finishes must still leave a visible
			// trail so "looking into it" never ends in total silence.
			m.appendTranscript(dimStyle.Render("  (no reply — model finished without text)"))
		}

	case events.ConversationCleared:
		m.clearTranscript()
		m.followChat = true
		m.appendTranscript(dimStyle.Render("  Conversation cleared."))

	case events.TurnMetrics:
		m.lastMetrics = e
		// Idle no-speech timeouts restart listening every ~listen_timeout
		// seconds. Logging each one floods Activity without helping the user.
		if e.Outcome == "timed_out" {
			break
		}
		m.appendActivity("turn", e.Outcome, e.PlaybackCompleteElapsed)
		if line := formatTurnMetrics(e); line != "" {
			m.appendTranscript(dimStyle.Render("    " + line))
		}

	case events.Error:
		m.appendActivity("error", e.Stage, 0)
		msg := e.Message
		if e.Stage != "" {
			msg = "[" + e.Stage + "] " + e.Message
		}
		m.setVoiceMode(anim.ModeError)
		m.setStatus("Error: "+msg, true)

	case events.Info:
		m.appendActivity("info", e.Message, 0)
		m.appendTranscript(dimStyle.Render("  " + e.Message))

	case events.ToolCallStarted:
		msg := "tool " + e.Name
		if e.Summary != "" {
			msg += " (" + e.Summary + ")"
		}
		m.appendActivity("tool", msg, 0)
		m.setStatus("🔧 "+msg+"...", false)
		m.appendTranscript(dimStyle.Render("  🔧 " + msg))

	case events.ToolCallFinished:
		msg := "tool " + e.Name + " done"
		if e.Err != "" {
			msg = "tool " + e.Name + " failed: " + e.Err
			m.setVoiceMode(anim.ModeError)
			m.setStatus("✗ "+msg, true)
			m.appendTranscript(dimStyle.Render("  ✗ " + msg))
		} else {
			if e.Preview != "" {
				msg += " → " + e.Preview
			}
			m.setVoiceMode(anim.ModeThinking)
			m.setStatus(m.agentName+" thinking", false)
			m.appendTranscript(dimStyle.Render("  ✓ " + msg))
		}
		m.appendActivity("tool", msg, 0)
	}
}

// formatTurnMetrics compacts a turn's latency milestones into one dim
// trailing line under the turn (the status bar is overwritten by the next
// listening state almost immediately, so timings live in the transcript).
func formatTurnMetrics(e events.TurnMetrics) string {
	var parts []string
	if e.ModelCompleteElapsed > 0 {
		parts = append(parts, "model "+formatSeconds(e.ModelCompleteElapsed))
	}
	if e.FirstAudioReadyElapsed > 0 {
		parts = append(parts, "voice "+formatSeconds(e.FirstAudioReadyElapsed))
	}
	if e.PlaybackCompleteElapsed > 0 {
		parts = append(parts, "spoke "+formatSeconds(e.PlaybackCompleteElapsed))
	}
	return strings.Join(parts, " · ")
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
}

// renderUserTurn and renderAgentTurn are the single rendering path for both
// live events and replayed session history, so the two cannot drift apart.
func renderUserTurn(text string) string {
	body := userStyle.Render("› You") + "\n" + normalStyle.Render(text)
	return userBubbleStyle.Render(body)
}

func renderAgentTurn(name, text string) string {
	body := samanthaStyle.Render("● "+name) + "\n" + normalStyle.Render(text)
	return agentBubbleStyle.Render(body)
}

func (m conversationModel) View() string {
	if !m.ready {
		return "\n  " + headerStyle.Render("Preparing conversation…") + "\n"
	}

	styles := m.animStyles()
	w := max(m.width, 1)

	// Clean header: name, tabs, compact EQ chip.
	left := headerStyle.Render(m.agentName)
	if badge := ttsBadgeLabel(m.cfg); badge != "" {
		left += "  " + chipMutedStyle.Render(badge)
	}
	left += "  " + m.renderTabs()
	if m.sessionID != "" {
		left += "  " + dimStyle.Render(shortSessionID(m.sessionID))
	}
	right := ""
	if m.voiceMode != anim.ModeIdle {
		right = anim.CompactMeter(m.voiceMode, m.voiceFrame, m.voiceLevel(), m.status, styles, m.reducedMotion)
	} else if m.status != "" {
		style := statusStyle
		if m.statusErr {
			style = errorStyle
		}
		right = style.Render(m.status)
	}
	headerInner := left
	if right != "" {
		pad := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
		if pad < 1 {
			pad = 1
		}
		headerInner = left + strings.Repeat(" ", pad) + right
	}
	header := ansi.Truncate(headerInner, w, "…")

	rule := lipgloss.NewStyle().Foreground(m.inputBorderColor()).Render(strings.Repeat("─", w))

	voiceStrip := ""
	if m.voiceMode != anim.ModeIdle && m.height >= 14 {
		voiceStrip = anim.Stage(m.voiceMode, m.voiceFrame, m.voiceLevel(), w, m.status, styles, m.reducedMotion)
		if voiceStrip != "" {
			voiceStrip += "\n"
		}
	}

	// Capture stays armed while voice input is paused; wording must not claim
	// the OS microphone was released.
	micChip := chipMutedStyle.Render("mic off")
	if m.voiceOn() {
		micChip = chipStyle.Render("mic on")
		if m.voiceMode == anim.ModeHearing {
			micChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorHearing).Bold(true).Padding(0, 1).Render("recording")
		}
	}
	outChip := chipMutedStyle.Render("audio n/a")
	if m.outputAvailable {
		if m.outputMuted {
			outChip = chipMutedStyle.Render("audio off")
		} else if m.voiceMode == anim.ModeSpeaking {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorSpeak).Bold(true).Padding(0, 1).Render("speaking")
		} else {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorAgent).Bold(true).Padding(0, 1).Render("audio on")
		}
	}
	footerLeft := "  " + micChip + " " + outChip
	if m.liveSpeakerStatsKnown {
		label := liveSpeakerFooterLabel(m.liveSpeakerStats)
		footerLeft += " " + liveSpeakerStatusStyle(m.liveSpeakerStats.Status).Render(label)
	}
	activeViewport := m.activeViewport()
	if activeViewport.TotalLineCount() > activeViewport.VisibleLineCount() {
		footerLeft += " " + chipMutedStyle.Render(fmt.Sprintf("%d%%", int(activeViewport.ScrollPercent()*100)))
	}
	footerHelp := m.vimFooterHelp()
	footerText := footerLeft
	switch {
	case m.vim.enabled:
		// Modal controls are the primary interaction contract. Keep them visible
		// at medium widths, then include device state when the terminal has room.
		compactHelp := "  " + m.vimCompactFooterHelp()
		footerText = compactHelp
		if m.width >= lipgloss.Width(compactHelp)+lipgloss.Width(footerLeft)+4 {
			footerText += strings.Repeat(" ", m.width-lipgloss.Width(compactHelp)-lipgloss.Width(footerLeft)) + footerLeft
		}
	case m.width >= lipgloss.Width(footerLeft)+lipgloss.Width(footerHelp)+4:
		footerText += strings.Repeat(" ", m.width-lipgloss.Width(footerLeft)-lipgloss.Width(footerHelp)) + dimStyle.Render(footerHelp)
	case m.width >= 60:
		footerText += dimStyle.Render("  ·  ^G mic  ^O audio  ^T switch")
	default:
		footerText += dimStyle.Render("  ^G  ^O  ^T")
	}
	footer := ansi.Truncate(footerText, w, "…")

	content := m.viewport.View()
	if m.activityFocused {
		content = m.activityViewport.View()
	}

	inputLabel := "Your message"
	switch {
	case m.voiceMode == anim.ModeHearing:
		inputLabel = hearingStyle.Render("● Hearing you — type to interrupt")
	case m.voiceMode == anim.ModeListening:
		inputLabel = headerStyle.Render("◎ Listening — type anytime")
	case m.voiceMode == anim.ModeSpeaking || m.voiceMode == anim.ModeSynthesizing:
		inputLabel = speakStyle.Render("◉ Speaking — type to barge in")
	case m.turnState == turnVoiceListening:
		inputLabel = headerStyle.Render("🎙 Listening — type to interrupt")
	case m.turnState == turnVoiceResponding || m.turnState == turnVoiceCanceling || m.turnState == turnTextRunning:
		inputLabel = thinkStyle.Render("✦ Responding — keep drafting")
	default:
		inputLabel = dimStyle.Render(inputLabel)
	}
	inputLabel = m.vimInputLabel(inputLabel)
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.inputBorderColor()).
		Padding(0, 1).
		Render(m.input.View())

	palette := m.renderCommandPalette()
	if palette != "" {
		content += "\n" + palette
	}

	return header + "\n" + rule + "\n" +
		voiceStrip +
		content + "\n" +
		ansi.Truncate(inputLabel, w, "…") + "\n" +
		inputBox + "\n" +
		footer
}

func (m conversationModel) renderTabs() string {
	inactive := lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)
	active := lipgloss.NewStyle().Foreground(colorBg).Background(colorSelect).Bold(true).Padding(0, 1)
	chat := inactive.Render("Chat")
	activity := inactive.Render("Activity")
	if m.activityFocused {
		activity = active.Render("Activity")
	} else {
		chat = active.Render("Chat")
	}
	return chat + " " + activity
}

func shortSessionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}
