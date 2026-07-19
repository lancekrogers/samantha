package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meetinglog"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

const (
	meetingTickInterval = 100 * time.Millisecond
	meetingMaxLines     = 500
)

// MeetingOpts configures the interactive meeting recorder TUI.
type MeetingOpts struct {
	Ctx         context.Context
	Cancel      context.CancelFunc
	Capture     listen.Resetter
	Provider    stt.Provider
	Writer      *meetinglog.Writer
	Description string
	Path        string
	StopPhrases map[string]bool // normalized phrase → true
}

type meetingPhaseMsg string
type meetingLevelMsg float64
type meetingPartialMsg string
type meetingUtteranceMsg listen.Utterance
type meetingErrorMsg struct{ err error }
type meetingLoopDoneMsg struct{ err error }
type meetingTickMsg time.Time

// meetingModel is the live recorder screen: EQ strip + scrolling transcript.
type meetingModel struct {
	opts MeetingOpts

	width  int
	height int
	ready  bool

	viewport viewport.Model
	lines    []string

	voiceMode     anim.Mode
	voiceFrame    int
	inputLevel    float64
	partial       string
	status        string
	statusErr     bool
	reducedMotion bool
	voiceTicking  bool

	started    time.Time
	utterances int
	errors     int
	quitting   bool
	loopDone   bool
	loopErr    error
}

// RunMeeting launches the Bubble Tea meeting recorder. It runs listen.Loop in
// the background, paints the same voice EQ as the conversation TUI, and
// returns after the loop stops (Ctrl+C, q, stop phrase, or failure).
func RunMeeting(opts MeetingOpts) error {
	if opts.Ctx == nil {
		opts.Ctx = context.Background()
	}
	if opts.Cancel == nil {
		var cancel context.CancelFunc
		opts.Ctx, cancel = context.WithCancel(opts.Ctx)
		opts.Cancel = cancel
	}
	forceTUIColorProfile()

	m := meetingModel{
		opts:          opts,
		started:       time.Now(),
		voiceMode:     anim.ModeListening,
		status:        "Listening",
		reducedMotion: anim.ReducedMotion(),
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(opts.Ctx))
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(meetingModel); ok && fm.loopErr != nil {
		return fm.loopErr
	}
	return nil
}

func (m meetingModel) Init() tea.Cmd {
	return tea.Batch(m.startLoop(), meetingTickCmd())
}

func meetingTickCmd() tea.Cmd {
	return tea.Tick(meetingTickInterval, func(t time.Time) tea.Msg { return meetingTickMsg(t) })
}

// msgCh is filled by the listen goroutine; wait drains it one at a time.
func (m *meetingModel) startLoop() tea.Cmd {
	ch := make(chan tea.Msg, 256)
	opts := m.opts

	go func() {
		defer close(ch)
		sink := &meetingUISink{ch: ch, phrases: opts.StopPhrases, stop: opts.Cancel, writer: opts.Writer}
		hooks := listen.Hooks{
			OnPhase: func(phase string) {
				trySendMeeting(ch, meetingPhaseMsg(phase))
			},
			OnLevel: func(level float64) {
				trySendMeeting(ch, meetingLevelMsg(level))
			},
			OnPartial: func(text string) {
				trySendMeeting(ch, meetingPartialMsg(text))
			},
		}
		err := listen.LoopWithHooks(opts.Ctx, opts.Capture, opts.Provider, sink, hooks)
		trySendMeeting(ch, meetingLoopDoneMsg{err: err})
	}()

	// Store channel via closure chain: return a wait that drains ch.
	return waitMeetingCh(ch)
}

func waitMeetingCh(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return meetingLoopDoneMsg{}
		}
		return meetingChMsg{msg: msg, ch: ch}
	}
}

// meetingChMsg wraps a listen event and the channel so Update can re-arm wait.
type meetingChMsg struct {
	msg tea.Msg
	ch  <-chan tea.Msg
}

func trySendMeeting(ch chan<- tea.Msg, msg tea.Msg) {
	select {
	case ch <- msg:
	default:
		// Drop under back-pressure — levels/partials are advisory.
	}
}

type meetingUISink struct {
	ch      chan<- tea.Msg
	phrases map[string]bool
	stop    context.CancelFunc
	writer  *meetinglog.Writer
}

func (s *meetingUISink) OnUtterance(u listen.Utterance) error {
	if s.phrases != nil && s.phrases[normalizeMeetingStop(u.Text)] {
		if s.stop != nil {
			s.stop()
		}
		return nil // stop phrases are not written
	}
	if s.writer != nil {
		if err := s.writer.OnUtterance(u); err != nil {
			return err
		}
	}
	trySendMeeting(s.ch, meetingUtteranceMsg(u))
	return nil
}

func (s *meetingUISink) OnTimeout() error {
	if s.writer != nil {
		return s.writer.OnTimeout()
	}
	return nil
}

func (s *meetingUISink) OnError(err error) error {
	if s.writer != nil {
		if werr := s.writer.OnError(err); werr != nil {
			return werr
		}
	}
	trySendMeeting(s.ch, meetingErrorMsg{err: err})
	return nil
}

func normalizeMeetingStop(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimRight(s, ".,!?"))
}

func (m meetingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.reflow()
		return m, nil

	case meetingChMsg:
		inner := msg.msg
		var cmd tea.Cmd
		m, cmd = m.handleListenMsg(inner)
		if m.loopDone || m.quitting {
			return m, tea.Batch(cmd, tea.Quit)
		}
		return m, tea.Batch(cmd, waitMeetingCh(msg.ch), m.ensureVoiceTick())

	case meetingTickMsg:
		m.voiceFrame++
		m.inputLevel *= 0.82
		if m.inputLevel < 0.02 {
			m.inputLevel = 0
		}
		if m.reducedMotion || m.voiceMode == anim.ModeIdle || m.quitting {
			m.voiceTicking = false
			return m, nil
		}
		return m, meetingTickCmd()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			if m.opts.Cancel != nil {
				m.opts.Cancel()
			}
			// Wait for loop done via channel; if already done, quit now.
			if m.loopDone {
				return m, tea.Quit
			}
			return m, nil
		case "pgup", "pgdown", "up", "down", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

func (m meetingModel) handleListenMsg(msg tea.Msg) (meetingModel, tea.Cmd) {
	switch msg := msg.(type) {
	case meetingPhaseMsg:
		switch string(msg) {
		case "listening":
			m.voiceMode = anim.ModeListening
			m.status = "Listening"
			m.statusErr = false
			m.partial = ""
		case "hearing":
			m.voiceMode = anim.ModeHearing
			m.status = "Hearing speech"
			m.statusErr = false
		case "transcribing":
			m.voiceMode = anim.ModeTranscribing
			m.status = "Transcribing"
			m.statusErr = false
		}
	case meetingLevelMsg:
		level := float64(msg)
		if level < 0 {
			level = 0
		}
		if level > 1 {
			level = 1
		}
		if level > m.inputLevel {
			m.inputLevel = level
		} else {
			m.inputLevel = m.inputLevel*0.55 + level*0.45
		}
		if m.voiceMode == anim.ModeListening && m.inputLevel > 0.12 {
			m.voiceMode = anim.ModeHearing
			m.status = "Hearing speech"
		}
	case meetingPartialMsg:
		m.partial = string(msg)
		if m.voiceMode == anim.ModeListening {
			m.voiceMode = anim.ModeHearing
		}
	case meetingUtteranceMsg:
		u := listen.Utterance(msg)
		m.utterances++
		m.partial = ""
		m.voiceMode = anim.ModeListening
		m.status = "Listening"
		m.appendLine(fmt.Sprintf("%s  %s", dimStyle.Render(u.At.Format("15:04:05")), normalStyle.Render(u.Text)))
	case meetingErrorMsg:
		m.errors++
		m.statusErr = true
		m.status = "Transcription error (retrying)"
		m.appendLine(errorStyle.Render(fmt.Sprintf("  error: %v", msg.err)))
	case meetingLoopDoneMsg:
		m.loopDone = true
		m.loopErr = msg.err
		m.voiceMode = anim.ModeIdle
		if msg.err != nil {
			m.statusErr = true
			m.status = msg.err.Error()
		} else {
			m.status = "Stopped"
		}
		m.quitting = true
	}
	return m, nil
}

func (m *meetingModel) ensureVoiceTick() tea.Cmd {
	if m.reducedMotion || m.voiceMode == anim.ModeIdle || m.voiceTicking || m.quitting {
		return nil
	}
	m.voiceTicking = true
	return meetingTickCmd()
}

func (m *meetingModel) reflow() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// header + rule + stage(~5) + partial + rule + footer ≈ 12 rows
	chrome := 12
	vpH := max(m.height-chrome, 3)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpH)
		m.ready = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpH
	}
	m.refreshTranscript()
}

func (m *meetingModel) appendLine(line string) {
	follow := !m.ready || m.viewport.AtBottom()
	m.lines = append(m.lines, line)
	if len(m.lines) > meetingMaxLines {
		m.lines = m.lines[len(m.lines)-meetingMaxLines:]
	}
	m.refreshTranscript()
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *meetingModel) refreshTranscript() {
	if !m.ready {
		return
	}
	content := strings.Join(m.lines, "\n")
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

func (m meetingModel) View() string {
	if !m.ready {
		return "\n  " + headerStyle.Render("Starting meeting recorder…") + "\n"
	}
	w := max(m.width, 1)
	styles := voiceAnimStyles()

	header := headerStyle.Render("Meeting") + "  " + normalStyle.Render(m.opts.Description)
	header = ansi.Truncate(header, w, "…")

	pathLine := dimStyle.Render(ansi.Truncate("  "+m.opts.Path, w, "…"))
	rule := lipgloss.NewStyle().Foreground(m.meterBorderColor()).Render(strings.Repeat("─", w))

	stage := anim.Stage(m.voiceMode, m.voiceFrame, m.inputLevel, w, m.status, styles, m.reducedMotion)
	if stage != "" {
		stage += "\n"
	}

	partial := ""
	if m.partial != "" {
		partial = dimStyle.Render("  … ") + hearingStyle.Render(ansi.Truncate(m.partial, max(w-4, 1), "…")) + "\n"
	}

	elapsed := time.Since(m.started).Round(time.Second)
	footer := fmt.Sprintf("  %s  ·  %d utterances", formatMeetingDuration(elapsed), m.utterances)
	if m.errors > 0 {
		footer += fmt.Sprintf("  ·  %d errors", m.errors)
	}
	footer += "  ·  q / Ctrl+C stop  ·  say \"stop recording\""
	footer = dimStyle.Render(ansi.Truncate(footer, w, "…"))

	return header + "\n" + pathLine + "\n" + rule + "\n" +
		stage + partial +
		m.viewport.View() + "\n" +
		rule + "\n" + footer
}

func (m meetingModel) meterBorderColor() lipgloss.Color {
	switch m.voiceMode {
	case anim.ModeHearing:
		return colorHearing
	case anim.ModeListening:
		return colorAccent
	case anim.ModeTranscribing:
		return colorStatus
	case anim.ModeError:
		return colorError
	default:
		return colorDim
	}
}

func formatMeetingDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
