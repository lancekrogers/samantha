package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

// Rows of chrome around the viewport: header, rule, input line, footer.
const conversationChromeRows = 4

// conversationModel renders the live conversation screen: a scrollable
// transcript viewport, a persistent status indicator, and an always-focused
// input line. It renders purely from injected state — turn dispatch and event
// bus wiring are layered on by later slices.
type conversationModel struct {
	agentName string

	width  int
	height int
	ready  bool

	viewport viewport.Model
	input    textinput.Model

	transcript []string
	status     string
	statusErr  bool

	bridge      *eventBridge
	lastMetrics events.TurnMetrics

	deps          conversationDeps
	turnState     turnState
	turnCancel    func()
	pendingText   string
	voiceEnabled  bool
	voiceFailures int
	quitting      bool
}

func newConversation(agentName string) conversationModel {
	if agentName == "" {
		agentName = "Samantha"
	}

	input := textinput.New()
	input.Prompt = "> "
	input.Focus()

	return conversationModel{
		agentName: agentName,
		input:     input,
	}
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
		// PgUp/PgDn (and home/end) scroll the transcript; every other key —
		// including ↑/↓ for cursor movement — belongs to the input line, so
		// typing never needs a mode switch.
		switch msg.String() {
		case "enter":
			return m, m.handleSubmit()
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height

	vpHeight := max(height-conversationChromeRows, 1)
	if !m.ready {
		m.viewport = viewport.New(width, vpHeight)
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = vpHeight
	}
	m.input.Width = max(width-len(m.input.Prompt)-3, 10)
	m.refreshContent()
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
		m.appendTranscript(renderUserTurn(e.Text))

	case events.ThinkingStarted:
		m.setStatus("● "+m.agentName+" thinking...", false)

	case events.GeneratingVoice:
		m.setStatus("● Synthesizing voice...", false)

	case events.SpeakingStarted:
		m.setStatus("● Speaking...", false)

	case events.SpeakingComplete:
		m.setStatus("", false)

	case events.SpeakingInterrupted:
		m.setStatus("speech interrupted ("+e.Reason+")", false)

	case events.TurnInterrupted:
		m.setStatus("turn interrupted ("+e.Reason+")", false)

	case events.ResponseReady:
		m.appendTranscript(renderAgentTurn(m.agentName, e.Response), "")

	case events.ConversationCleared:
		m.clearTranscript()
		m.appendTranscript(dimStyle.Render("  Conversation cleared."))

	case events.TurnMetrics:
		m.lastMetrics = e
		if line := formatTurnMetrics(e); line != "" {
			m.appendTranscript(dimStyle.Render("    " + line))
		}

	case events.Error:
		msg := e.Message
		if e.Stage != "" {
			msg = "[" + e.Stage + "] " + e.Message
		}
		m.setStatus("Error: "+msg, true)

	case events.Info:
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
	return "  " + userStyle.Render("You:") + " " + text
}

func renderAgentTurn(name, text string) string {
	return "  " + samanthaStyle.Render(name+":") + " " + text
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

	header := "  " + headerStyle.Render(m.agentName)
	if status != "" {
		header += "  " + style.Render(status)
	}

	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))
	footer := dimStyle.Render("  pgup/pgdn scroll • /clear • /voice • ctrl+c quit")

	// The prompt glyph shows the input source: the mic while a voice turn is
	// listening, a plain prompt otherwise. Typing never needs a mode switch.
	input := m.input
	if m.turnState == turnVoiceListening && m.voiceOn() {
		input.Prompt = "🎙 > "
	}

	return header + "\n" + rule + "\n" +
		m.viewport.View() + "\n" +
		"  " + input.View() + "\n" +
		footer
}
