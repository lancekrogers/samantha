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
)

const conversationInputHeight = 3

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
}

func newConversation(agentName string) conversationModel {
	if agentName == "" {
		agentName = "Samantha"
	}

	input := textarea.New()
	input.Placeholder = "Type a message…"
	input.CharLimit = 1000
	input.ShowLineNumbers = false
	input.KeyMap.InsertNewline.SetKeys("ctrl+j")
	input.KeyMap.InsertNewline.SetHelp("ctrl+j", "new line")
	input.SetHeight(conversationInputHeight)
	input.Focus()

	return conversationModel{
		agentName:      agentName,
		input:          input,
		canCancelVoice: &atomic.Bool{},
		startedAt:      time.Now(),
	}
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
		return m, nil

	case busEventMsg:
		m.handleEvent(msg.event)
		return m, m.rearm()

	case voiceTurnDoneMsg:
		return m, m.handleVoiceTurnDone(msg)

	case textTurnDoneMsg:
		return m, m.handleTextTurnDone(msg)

	case voiceRetryMsg:
		return m, m.handleVoiceRetry()

	case tea.KeyMsg:
		// Page keys scroll history. Editing keys stay with the always-focused
		// composer so multiline drafting never needs a mode switch.
		switch msg.String() {
		case "enter":
			return m, m.handleSubmit()
		case "ctrl+g":
			return m, m.toggleInputMuted()
		case "ctrl+o":
			m.toggleOutputMuted()
			return m, nil
		case "ctrl+t":
			m.activityFocused = !m.activityFocused
			return m, nil
		case "esc":
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
			if m.activityFocused {
				m.activeViewport().GotoTop()
				return m, nil
			}
		case "end":
			if m.activityFocused {
				m.activeViewport().GotoBottom()
				return m, nil
			}
		case "up", "down":
			if m.activityFocused {
				return m.updateScroll(msg)
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		if !tea.MouseEvent(msg).IsWheel() {
			return m, nil
		}
		return m.updateScroll(msg)
	}

	return m, nil
}

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height

	inputHeight := conversationInputHeight
	if height < 12 {
		inputHeight = 1
	}
	// Header + rule + label + input border + footer consume six rows in
	// addition to the textarea's own height.
	vpHeight := max(height-inputHeight-6, 1)
	if !m.ready {
		m.viewport = viewport.New(max(width, 1), vpHeight)
		m.activityViewport = viewport.New(max(width, 1), vpHeight)
		m.ready = true
	} else {
		m.viewport.Width = max(width, 1)
		m.viewport.Height = vpHeight
		m.activityViewport.Width = max(width, 1)
		m.activityViewport.Height = vpHeight
	}
	m.input.SetWidth(max(width-4, 1))
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

func (m *conversationModel) refreshContent() {
	if !m.ready {
		return
	}
	content := strings.Join(m.transcript, "\n")
	// lipgloss wraps to width so long turns don't overflow the viewport.
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
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
	case events.STTPhase:
		m.appendActivity("input", e.Phase, e.Elapsed)
		switch e.Phase {
		case "listening":
			m.setStatus("🎙 Listening...", false)
		case "hearing":
			m.setStatus("🎙 Hearing you...", false)
		case "transcribing":
			m.setStatus("● Transcribing...", false)
		}

	case events.TranscriptPartial:
		m.setStatus("🎙 "+e.Text, false)

	case events.UserInput:
		m.appendActivity("input", "final", 0)
		m.appendTranscript(renderUserTurn(e.Text))

	case events.ThinkingStarted:
		m.appendActivity("model", "started", 0)
		m.setStatus("● "+m.agentName+" thinking...", false)

	case events.ResponseStreamingStarted:
		m.appendActivity("model", "first response", e.Elapsed)

	case events.ThinkingComplete:
		m.appendActivity("model", "complete", e.Elapsed)

	case events.SpeechSegmentReady:
		m.appendActivity("voice", "segment ready", 0)

	case events.GeneratingVoice:
		m.appendActivity("voice", "synthesizing", 0)
		m.setStatus("● Synthesizing voice...", false)

	case events.VoiceGenerated:
		m.appendActivity("voice", "generated", e.Elapsed)

	case events.SpeakingStarted:
		m.appendActivity("output", "playing", 0)
		m.setStatus("● Speaking...", false)

	case events.SpeakingComplete:
		m.appendActivity("output", "complete", e.Elapsed)
		m.setStatus("", false)

	case events.SpeakingInterrupted:
		m.appendActivity("output", "interrupted: "+e.Reason, 0)
		m.setStatus("speech interrupted ("+e.Reason+")", false)

	case events.TurnInterrupted:
		m.appendActivity("turn", "interrupted: "+e.Reason, 0)
		m.setStatus("turn interrupted ("+e.Reason+")", false)

	case events.ResponseReady:
		m.appendActivity("turn", "response ready", 0)
		// Text-only / no-TTS turns never emit SpeakingComplete; clear the
		// thinking status here so it does not stick after a successful reply.
		m.setStatus("", false)
		m.appendTranscript(renderAgentTurn(m.agentName, e.Response), "")

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
		m.setStatus("Error: "+msg, true)

	case events.Info:
		m.appendActivity("info", e.Message, 0)
		m.appendTranscript(dimStyle.Render("  " + e.Message))
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

	status := m.status
	style := statusStyle
	if m.statusErr {
		style = errorStyle
	}

	header := "  " + headerStyle.Render(m.agentName) + "  " + m.renderTabs()
	if m.sessionID != "" {
		header += "  " + dimStyle.Render("session "+shortSessionID(m.sessionID))
	}
	if status != "" {
		header += "  " + style.Render(status)
	}
	header = ansi.Truncate(header, max(m.width, 1), "…")

	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))
	micState := "mic off"
	if m.voiceOn() {
		micState = "mic on"
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
	footerHelp := "enter send  ^J newline  ^G mic  ^O audio  ^T switch  PgUp/PgDn scroll"
	footerText := footerLeft
	switch {
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
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("14")).
		Padding(0, 1).
		Render(m.input.View())

	return header + "\n" + rule + "\n" +
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
