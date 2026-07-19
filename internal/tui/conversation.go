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
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

const conversationInputHeight = 3

// voiceTickInterval drives the conversation meter animation (~10 fps).
const voiceTickInterval = 100 * time.Millisecond

// voicePanelRows is the vertical space reserved for the animated voice panel
// when an active voice mode is showing art under the header rule.
const voicePanelRows = 4

// voiceTickMsg advances ambient voice animations.
type voiceTickMsg time.Time

// conversationModel renders the live conversation screen: a scrollable
// transcript viewport, a persistent status indicator, and an always-focused
// input line. It renders purely from injected state — turn dispatch and event
// bus wiring are layered on by later slices.
type conversationModel struct {
	agentName string

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
	startedAt       time.Time
	sessionID       string
	inputDevice     string
	outputDevice    string
	voiceFailures   int
	quitting        bool

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
			return m, nil
		case "ctrl+end":
			m.activeViewport().GotoBottom()
			return m, nil
		case "home":
			// Activity always jumps; Chat only when the composer is empty so
			// bare Home/End still navigate the transcript without fighting
			// line-start/end editing while drafting.
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoTop()
				return m, nil
			}
		case "end":
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoBottom()
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
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Header + rule + label + input border + footer consume six rows in
	// addition to the textarea's own height. An active voice panel adds a few
	// more. Command matches consume only the rows currently available above
	// the composer.
	chrome := 6
	if m.voiceMode != anim.ModeIdle && m.height >= 16 {
		chrome += voicePanelRows
	}
	vpHeight := max(m.height-inputHeight-chrome-m.commandPaletteRows(), 1)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpHeight)
		m.activityViewport = viewport.New(max(m.width, 1), vpHeight)
		m.ready = true
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
	return m, cmd
}

// appendTranscript adds rendered lines to the transcript, following the tail
// only when the user has not scrolled up to review history.
func (m *conversationModel) appendTranscript(lines ...string) {
	follow := !m.ready || m.viewport.AtBottom()
	m.transcript = append(m.transcript, lines...)
	m.refreshContent()
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *conversationModel) clearTranscript() {
	m.transcript = nil
	m.refreshContent()
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
	return anim.Styles{
		Tip:    lipgloss.NewStyle().Foreground(colorAccent).Bold(true),
		Mid:    statusStyle,
		Core:   dimStyle,
		Muted:  dimStyle,
		Label:  statusStyle,
		Error:  errorStyle,
		Accent: headerStyle,
	}
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
// following the tail unless the user has scrolled up to review history.
func (m *conversationModel) appendStreamingDelta(text string) {
	follow := !m.ready || m.viewport.AtBottom()
	m.streamingAgent += text
	m.refreshContent()
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *conversationModel) appendActivity(stage, detail string, elapsed time.Duration) {
	follow := !m.ready || m.activityViewport.AtBottom()
	m.activity = append(m.activity, activityEntry{
		at: time.Since(m.startedAt), stage: stage, detail: detail, elapsed: elapsed,
	})
	if len(m.activity) > 500 {
		m.activity = append([]activityEntry(nil), m.activity[len(m.activity)-500:]...)
	}
	m.refreshActivity()
	if follow {
		m.activityViewport.GotoBottom()
	}
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
		m.appendTranscript(renderUserTurn(e.Text))

	case events.ThinkingStarted:
		// Start a fresh streaming buffer; a prior turn's leftover (e.g. after an
		// interrupt that skipped ResponseReady) must not bleed into this reply.
		// Re-render so any stale live partial leaves the viewport immediately.
		if m.streamingAgent != "" {
			m.streamingAgent = ""
			m.refreshContent()
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
		} else {
			m.refreshContent()
		}

	case events.ConversationCleared:
		m.clearTranscript()
		m.appendTranscript(dimStyle.Render("  Conversation cleared."))

	case events.TurnMetrics:
		m.appendActivity("turn", e.Outcome, e.PlaybackCompleteElapsed)
		m.lastMetrics = e
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
	return "  " + userStyle.Render("› You") + "\n  " + text
}

func renderAgentTurn(name, text string) string {
	return "  " + samanthaStyle.Render("● "+name) + "\n  " + text
}

func (m conversationModel) View() string {
	if !m.ready {
		return "\n  Preparing conversation...\n"
	}

	styles := m.animStyles()
	// Header keeps a compact live meter; full art sits under the rule when space allows.
	header := "  " + headerStyle.Render(m.agentName) + "  " + m.renderTabs()
	if m.sessionID != "" {
		header += "  " + dimStyle.Render("session "+shortSessionID(m.sessionID))
	}
	if m.voiceMode != anim.ModeIdle {
		statusStyleForMode := statusStyle
		if m.statusErr {
			statusStyleForMode = errorStyle
		} else {
			switch m.voiceMode {
			case anim.ModeHearing:
				statusStyleForMode = hearingStyle
			case anim.ModeSpeaking, anim.ModeSynthesizing:
				statusStyleForMode = speakStyle
			}
		}
		styles.Label = statusStyleForMode
		meter := anim.CompactMeter(m.voiceMode, m.voiceFrame, m.voiceLevel(), m.status, styles, m.reducedMotion)
		if meter != "" {
			header += "  " + meter
		}
	} else if m.status != "" {
		style := statusStyle
		if m.statusErr {
			style = errorStyle
		}
		header += "  " + style.Render(m.status)
	}
	header = ansi.Truncate(header, max(m.width, 1), "…")

	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))

	voiceStrip := ""
	if m.voiceMode != anim.ModeIdle && m.height >= 16 {
		voiceStrip = anim.Panel(m.voiceMode, m.voiceFrame, m.voiceLevel(), max(m.width, 1), m.status, styles, m.reducedMotion)
		if voiceStrip != "" {
			voiceStrip += "\n"
		}
	}

	// Capture stays armed while voice input is paused; wording must not claim
	// the OS microphone was released.
	micState := "voice input off"
	if m.voiceOn() {
		micState = "voice input on"
	}
	outputState := "audio unavailable"
	if m.outputAvailable {
		outputState = "audio on"
		if m.outputMuted {
			outputState = "audio off"
		}
	}
	footerLeft := "  " + micState + "  •  " + outputState
	activeViewport := m.activeViewport()
	if activeViewport.TotalLineCount() > activeViewport.VisibleLineCount() {
		footerLeft += fmt.Sprintf("  •  %d%%", int(activeViewport.ScrollPercent()*100))
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
		footerText += strings.Repeat(" ", m.width-lipgloss.Width(footerLeft)-lipgloss.Width(footerHelp)) + footerHelp
	case m.width >= 60:
		footerText += "  •  ^G mic  ^O audio  ^T switch"
	default:
		footerText += "  ^G  ^O  ^T"
	}
	footer := dimStyle.Render(ansi.Truncate(footerText, max(m.width, 1), "…"))

	content := m.viewport.View()
	if m.activityFocused {
		content = m.activityViewport.View()
	}

	inputLabel := "Your message:"
	switch m.turnState {
	case turnVoiceListening:
		inputLabel = "🎙 Listening — type to interrupt:"
	case turnVoiceResponding, turnVoiceCanceling, turnTextRunning:
		inputLabel = "⏳ Samantha is responding — keep drafting:"
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
		dimStyle.Render(ansi.Truncate(inputLabel, max(m.width, 1), "…")) + "\n" +
		inputBox + "\n" +
		footer
}

func (m conversationModel) renderTabs() string {
	chat := dimStyle.Render("Chat")
	activity := dimStyle.Render("Activity")
	if m.activityFocused {
		activity = selectedStyle.Copy().Background(lipgloss.Color("236")).Padding(0, 1).Render("Activity")
	} else {
		chat = selectedStyle.Copy().Background(lipgloss.Color("236")).Padding(0, 1).Render("Chat")
	}
	return chat + "  " + activity
}

func shortSessionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}
