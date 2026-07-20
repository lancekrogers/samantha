package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/listen"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
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
			// Phase/level/partial are high-rate and droppable under backpressure.
			OnPhase:   func(phase string) { trySendMeeting(ch, meetingPhaseMsg(phase)) },
			OnLevel:   func(level float64) { trySendMeeting(ch, meetingLevelMsg(level)) },
			OnPartial: func(text string) { trySendMeeting(ch, meetingPartialMsg(text)) },
		}
		capture, provider := opts.Capture, opts.Provider
		if demoMeetingEnabled() {
			capture, provider = demoMeetingDeps()
		}
		err := listen.LoopWithHooks(opts.Ctx, capture, provider, sink, hooks)
		// Loop completion must not be dropped: UI uses it to exit cleanly.
		sendMeeting(ch, meetingLoopDoneMsg{err: err})
	}()

	return waitMeetingCh(ch)
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
