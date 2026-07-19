package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
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
	meetingNoteHeight   = 2
)

// MeetingOpts configures the interactive meeting recorder TUI.
type MeetingOpts struct {
	Ctx         context.Context
	Cancel      context.CancelFunc
	Capture     listen.Resetter
	Provider    stt.Provider
	Writer      *meetinglog.Writer
	Description string
	Path        string // .log path; JSONL is derived by the writer
	StopPhrases map[string]bool
	// Embedded is true when running inside the main Samantha App launcher
	// flow. Stop returns meetingDoneMsg instead of quitting the process.
	Embedded bool
}

type meetingPhaseMsg string
type meetingLevelMsg float64
type meetingPartialMsg string
type meetingUtteranceMsg listen.Utterance
type meetingErrorMsg struct{ err error }
type meetingLoopDoneMsg struct{ err error }
type meetingTickMsg time.Time
type meetingNoteErrMsg struct{ err error }

// meetingModel is the live recorder: EQ + timeline + note composer + bookmarks.
type meetingModel struct {
	opts MeetingOpts

	width  int
	height int
	ready  bool

	viewport viewport.Model
	note     textarea.Model
	lines    []string

	voiceMode     anim.Mode
	voiceFrame    int
	inputLevel    float64
	partial       string
	status        string
	statusErr     bool
	flash         string // brief action feedback ("★ bookmarked")
	flashUntil    time.Time
	reducedMotion bool
	voiceTicking  bool

	started    time.Time
	utterances int
	notes      int
	bookmarks  int
	errors     int
	quitting   bool
	loopDone   bool
	loopErr    error
}

// RunMeeting launches a standalone Bubble Tea meeting recorder (CLI path).
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
	opts.Embedded = false

	m := newEmbeddedMeeting()
	m.opts = opts
	m.started = time.Now()
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

func newEmbeddedMeeting() meetingModel {
	ta := textarea.New()
	ta.Placeholder = "Type a note and press Enter…  (Ctrl+B marks this moment important)"
	ta.CharLimit = 2000
	ta.ShowLineNumbers = false
	ta.SetHeight(meetingNoteHeight)
	ta.Focus()
	ta.KeyMap.InsertNewline.SetEnabled(false)
	return meetingModel{
		note:          ta,
		started:       time.Now(),
		voiceMode:     anim.ModeListening,
		status:        "Listening",
		reducedMotion: anim.ReducedMotion(),
	}
}

// beginRecording attaches deps and returns the cmd that starts the listen loop
// (used by the embedded main-menu flow after assets are ready).
func (m *meetingModel) beginRecording(opts MeetingOpts) tea.Cmd {
	m.opts = opts
	m.started = time.Now()
	m.voiceMode = anim.ModeListening
	m.status = "Listening"
	m.statusErr = false
	m.lines = nil
	m.utterances = 0
	m.notes = 0
	m.bookmarks = 0
	m.errors = 0
	m.quitting = false
	m.loopDone = false
	m.loopErr = nil
	m.partial = ""
	return tea.Batch(m.startLoop(), meetingTickCmd(), textarea.Blink)
}

func (m meetingModel) Init() tea.Cmd {
	// Standalone CLI: start the listen loop when deps are already on opts.
	if m.opts.Capture != nil && m.opts.Provider != nil {
		return tea.Batch(m.startLoop(), meetingTickCmd(), textarea.Blink)
	}
	return nil
}

func meetingTickCmd() tea.Cmd {
	return tea.Tick(meetingTickInterval, func(t time.Time) tea.Msg { return meetingTickMsg(t) })
}

func (m *meetingModel) startLoop() tea.Cmd {
	ch := make(chan tea.Msg, 256)
	opts := m.opts

	go func() {
		defer close(ch)
		sink := &meetingUISink{ch: ch, phrases: opts.StopPhrases, stop: opts.Cancel, writer: opts.Writer}
		hooks := listen.Hooks{
			OnPhase:   func(phase string) { trySendMeeting(ch, meetingPhaseMsg(phase)) },
			OnLevel:   func(level float64) { trySendMeeting(ch, meetingLevelMsg(level)) },
			OnPartial: func(text string) { trySendMeeting(ch, meetingPartialMsg(text)) },
		}
		capture, provider := opts.Capture, opts.Provider
		if demoMeetingEnabled() {
			capture, provider = demoMeetingDeps()
		}
		err := listen.LoopWithHooks(opts.Ctx, capture, provider, sink, hooks)
		trySendMeeting(ch, meetingLoopDoneMsg{err: err})
	}()

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

type meetingChMsg struct {
	msg tea.Msg
	ch  <-chan tea.Msg
}

func trySendMeeting(ch chan<- tea.Msg, msg tea.Msg) {
	select {
	case ch <- msg:
	default:
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
		return nil
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
		if m.loopDone {
			return m, tea.Batch(cmd, m.stopResultCmd())
		}
		return m, tea.Batch(cmd, waitMeetingCh(msg.ch), m.ensureVoiceTick())

	case meetingTickMsg:
		m.voiceFrame++
		m.inputLevel *= 0.82
		if m.inputLevel < 0.02 {
			m.inputLevel = 0
		}
		if !m.flashUntil.IsZero() && time.Now().After(m.flashUntil) {
			m.flash = ""
			m.flashUntil = time.Time{}
		}
		if m.reducedMotion || m.voiceMode == anim.ModeIdle || m.quitting {
			m.voiceTicking = false
			return m, nil
		}
		return m, meetingTickCmd()

	case meetingNoteErrMsg:
		m.statusErr = true
		m.status = "Failed to save note/bookmark"
		m.appendLine(errorStyle.Render(fmt.Sprintf("  write error: %v", msg.err)))
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m.requestStop()
		case "ctrl+b":
			return m.markImportant()
		case "enter":
			return m.submitNote()
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "esc":
			// Clear draft note; do not stop recording.
			if m.note.Value() != "" {
				m.note.SetValue("")
				return m, nil
			}
		}
		// Route remaining keys (including plain 'q') into the note field.
		var cmd tea.Cmd
		m.note, cmd = m.note.Update(msg)
		return m, cmd

	default:
		var cmd tea.Cmd
		m.note, cmd = m.note.Update(msg)
		return m, cmd
	}
}

func (m meetingModel) requestStop() (meetingModel, tea.Cmd) {
	m.quitting = true
	if m.opts.Cancel != nil {
		m.opts.Cancel()
	}
	if m.loopDone {
		return m, m.stopResultCmd()
	}
	return m, nil
}

// stopResultCmd leaves the recorder: embedded → launcher; standalone → Quit.
func (m meetingModel) stopResultCmd() tea.Cmd {
	if m.opts.Embedded {
		err := m.loopErr
		return func() tea.Msg { return meetingDoneMsg{Err: err} }
	}
	return tea.Quit
}

func (m meetingModel) submitNote() (meetingModel, tea.Cmd) {
	text := strings.TrimSpace(m.note.Value())
	if text == "" {
		return m, nil
	}
	if m.opts.Writer == nil {
		return m, nil
	}
	if err := m.opts.Writer.AddNote(text); err != nil {
		return m, func() tea.Msg { return meetingNoteErrMsg{err: err} }
	}
	m.notes++
	m.note.SetValue("")
	now := time.Now()
	m.appendLine(fmt.Sprintf("%s  %s %s",
		dimStyle.Render(now.Format("15:04:05")),
		hearingStyle.Render("📝"),
		normalStyle.Render(text),
	))
	m.setFlash("note saved")
	return m, nil
}

func (m meetingModel) markImportant() (meetingModel, tea.Cmd) {
	caption := strings.TrimSpace(m.note.Value())
	if m.opts.Writer == nil {
		return m, nil
	}
	if err := m.opts.Writer.AddBookmark("important", caption); err != nil {
		return m, func() tea.Msg { return meetingNoteErrMsg{err: err} }
	}
	m.bookmarks++
	m.note.SetValue("")
	now := time.Now()
	line := fmt.Sprintf("%s  %s",
		dimStyle.Render(now.Format("15:04:05")),
		speakStyle.Render("★ IMPORTANT"),
	)
	if caption != "" {
		line += "  " + normalStyle.Render(caption)
	}
	m.appendLine(line)
	m.setFlash("★ moment marked important")
	return m, nil
}

func (m *meetingModel) setFlash(s string) {
	m.flash = s
	m.flashUntil = time.Now().Add(2 * time.Second)
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
		m.appendLine(fmt.Sprintf("%s  %s %s",
			dimStyle.Render(u.At.Format("15:04:05")),
			headerStyle.Render("🎤"),
			normalStyle.Render(u.Text),
		))
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
	// header×2 + rules + stage + partial + note box + footer
	chrome := 14 + meetingNoteHeight
	vpH := max(m.height-chrome, 3)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpH)
		m.ready = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpH
	}
	m.note.SetWidth(max(m.width-4, 10))
	m.note.SetHeight(meetingNoteHeight)
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

	rec := errorStyle.Bold(true).Render("● REC")
	elapsed := formatMeetingDuration(time.Since(m.started).Round(time.Second))
	header := fmt.Sprintf("%s  %s  %s  %s",
		headerStyle.Render("Meeting"),
		normalStyle.Render(m.opts.Description),
		rec,
		dimStyle.Render(elapsed),
	)
	header = ansi.Truncate(header, w, "…")

	paths := m.opts.Path
	if m.opts.Writer != nil {
		paths = m.opts.Writer.Path() + "  +  " + m.opts.Writer.JSONLPath()
	}
	pathLine := dimStyle.Render(ansi.Truncate("  "+paths, w, "…"))
	rule := lipgloss.NewStyle().Foreground(m.meterBorderColor()).Render(strings.Repeat("─", w))

	stage := anim.Stage(m.voiceMode, m.voiceFrame, m.inputLevel, w, m.status, styles, m.reducedMotion)
	if stage != "" {
		stage += "\n"
	}

	partial := ""
	if m.partial != "" {
		partial = dimStyle.Render("  … ") + hearingStyle.Render(ansi.Truncate(m.partial, max(w-4, 1), "…")) + "\n"
	}

	// Action bar (menu of available commands).
	actions := []string{
		chipStyle.Render("Enter note"),
		lipgloss.NewStyle().Foreground(colorBg).Background(colorSpeak).Bold(true).Padding(0, 1).Render("Ctrl+B important"),
		chipMutedStyle.Render("Ctrl+C stop"),
	}
	actionBar := "  " + strings.Join(actions, "  ")
	if m.flash != "" {
		actionBar += "  " + statusStyle.Render(m.flash)
	}
	actionBar = ansi.Truncate(actionBar, w, "…")

	noteBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorHearing).
		Padding(0, 1).
		Render(m.note.View())

	footer := fmt.Sprintf("  %d spoken  ·  %d notes  ·  %d ★  ·  say \"stop recording\"",
		m.utterances, m.notes, m.bookmarks)
	if m.errors > 0 {
		footer += fmt.Sprintf("  ·  %d errors", m.errors)
	}
	footer = dimStyle.Render(ansi.Truncate(footer, w, "…"))

	return header + "\n" + pathLine + "\n" + rule + "\n" +
		stage + partial +
		m.viewport.View() + "\n" +
		rule + "\n" +
		actionBar + "\n" +
		noteBox + "\n" +
		footer
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
	mi := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mi, s)
	}
	return fmt.Sprintf("%02d:%02d", mi, s)
}
