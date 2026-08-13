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
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/tui/anim"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
)

const (
	meetingTickInterval = 100 * time.Millisecond
	meetingMaxLines     = 500
	meetingNoteHeight   = 2
)

// MeetingOpts configures the interactive meeting recorder TUI.
type MeetingOpts struct {
	Ctx           context.Context
	Cancel        context.CancelFunc
	Capture       listen.Resetter
	Provider      stt.Provider
	Writer        *meetinglog.Writer
	Description   string
	Path          string // .meeting bundle path
	StopPhrases   map[string]bool
	SpeakerStatus meeting.AnalysisStatus
	SpeakerError  string
	// LiveSpeaker is optional; when set, provisional labels attach to live rows.
	LiveSpeaker LiveSpeakerController
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

// meetingStopRequestedMsg is a durable signal that the user (or stop phrase)
// asked to end capture. It freezes the session clock before the listen loop
// drains, so the UI does not keep looking live while STT/diarize finish.
type meetingStopRequestedMsg struct{}

// meetingSessionPhase is the recorder lifecycle after beginRecording.
// Distinct from meetingPhaseMsg (STT listening/hearing/transcribing).
type meetingSessionPhase int

const (
	meetingSessionRecording meetingSessionPhase = iota
	meetingSessionStopping
	meetingSessionDiarizing
	meetingSessionDone
)

// meetingModel is the live recorder: EQ + timeline + note composer + bookmarks.
type meetingModel struct {
	opts MeetingOpts

	width  int
	height int
	ready  bool

	viewport viewport.Model
	note     textarea.Model
	// lines are structured so live speaker labels can revise a row without
	// re-parsing styled strings (design WI-1e881a P1).
	lines []meetingLine

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

	started        time.Time
	stoppedAt      time.Time // zero while still recording; freezes elapsed when set
	sessionPhase   meetingSessionPhase
	liveSpeaker    LiveSpeakerController
	liveStats      speaker.LiveStats
	liveStatsKnown bool
	// stickyLabel holds the last good speaker-N across brief empty stats.
	stickyLabel stickyLiveLabel
	// lastUtteranceID is the newest live speech row for async label attachment.
	lastUtteranceID int
	utterances      int
	notes           int
	bookmarks       int
	errors          int
	quitting        bool
	loopDone        bool
	loopErr         error
}

// RunMeeting launches a standalone Bubble Tea meeting recorder (CLI path).
// It returns when capture stops; closing the bundle, review, diarization, and
// routing are the caller's post-capture pipeline (see runMeetingRecord).
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
	m.sessionPhase = meetingSessionRecording
	// CLI path never calls beginRecording; wire live labels the same way so
	// samantha meeting record gets provisional speaker-N (not only launcher).
	m.liveSpeaker = opts.LiveSpeaker
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(meetingModel); ok && fm.loopErr != nil {
		return fm.loopErr
	}
	return nil
}

// MeetingAnalysisOutcome is a background diarization completion delivered to
// the standalone review screen and the CLI summary.
type MeetingAnalysisOutcome struct {
	Result meeting.AnalysisResult
	Err    error
}

// RunMeetingResults shows the standalone post-meeting review. A non-nil
// analysis channel marks speaker labels as updating in the background and
// folds the outcome into the view when it arrives before the user leaves.
func RunMeetingResults(summary meetinglog.Summary, analysis <-chan MeetingAnalysisOutcome) error {
	forceTUIColorProfile()
	results := newMeetingResults(summary)
	results.standalone = true
	results.analysisBusy = analysis != nil
	p := tea.NewProgram(standaloneMeetingResults{meetingResultsModel: results}, tea.WithAltScreen())
	if analysis != nil {
		go func() {
			if outcome, ok := <-analysis; ok {
				// Send after quit is a no-op; the CLI summary still reports.
				p.Send(meetingAnalysisDoneMsg{result: outcome.Result, err: outcome.Err})
			}
		}()
	}
	_, err := p.Run()
	return err
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
		note:      ta,
		started:   time.Now(),
		voiceMode: anim.ModeListening,
		status:    "Listening",
		// Speaker analysis is post-capture and opt-in; recording starts safely
		// with it disabled until a runtime provides an analyzer.
		opts:          MeetingOpts{SpeakerStatus: meeting.AnalysisDisabled},
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
	m.stoppedAt = time.Time{}
	m.sessionPhase = meetingSessionRecording
	m.liveSpeaker = opts.LiveSpeaker
	m.liveStatsKnown = false
	m.stickyLabel.Clear()
	m.lastUtteranceID = 0
	if m.opts.SpeakerStatus == "" {
		m.opts.SpeakerStatus = meeting.AnalysisDisabled
	}
	cmds := []tea.Cmd{m.startLoop(), meetingTickCmd(), textarea.Blink}
	if live := m.meetingLiveStartCmd(); live != nil {
		cmds = append(cmds, live)
	}
	return tea.Batch(cmds...)
}

func (m meetingModel) Init() tea.Cmd {
	// Standalone CLI: start the listen loop when deps are already on opts.
	// Live poll must start here too — beginRecording only runs on launcher path.
	if m.opts.Capture != nil && m.opts.Provider != nil {
		cmds := []tea.Cmd{m.startLoop(), meetingTickCmd(), textarea.Blink}
		if live := m.meetingLiveStartCmd(); live != nil {
			cmds = append(cmds, live)
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// meetingLiveStartCmd enables provisional labels and returns the first stats
// poll. Shared by CLI Init and embedded beginRecording.
func (m *meetingModel) meetingLiveStartCmd() tea.Cmd {
	if m.liveSpeaker == nil {
		return nil
	}
	m.liveSpeaker.SetEnabled(true)
	return liveSpeakerStatsCmd(m.liveSpeaker)
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
		switch {
		case demoMeetingSpeakersEnabled():
			capture, provider = demoMeetingSpeakerDeps()
		case demoMeetingEnabled():
			capture, provider = demoMeetingDeps()
		}
		err := listen.LoopWithHooks(opts.Ctx, capture, provider, sink, hooks)
		// Stop returns control immediately: diarization runs as a background
		// job — the App owns it after meetingDoneMsg, the CLI recorder after
		// RunMeeting returns (WI-162bbb R1).
		// Loop completion must not be dropped: UI uses it to exit cleanly.
		sendMeeting(ch, meetingLoopDoneMsg{err: err})
	}()

	return tea.Batch(waitMeetingCh(ch), demoMeetingSpeakerStatusCmds())
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
		m.appendSystemLine(errorStyle.Render(fmt.Sprintf("  write error: %v", msg.err)))
		return m, nil

	case liveSpeakerStatsMsg:
		m.liveStats = msg.stats
		m.liveStatsKnown = true
		// Always refresh sticky so a label heard between utterances still applies
		// to the next line (and holds across brief empty gaps).
		var label string
		if msg.stats.Status == speaker.LiveHealthy || msg.stats.Status == speaker.LiveRunning {
			label = m.stickyLabel.Observe(msg.stats.LastLabel, time.Now())
		}
		if m.lastUtteranceID > 0 && label != "" {
			m.setUtteranceLabel(m.lastUtteranceID, label)
		}
		if m.sessionPhase == meetingSessionRecording && m.liveSpeaker != nil {
			return m, liveSpeakerStatsCmd(m.liveSpeaker)
		}
		return m, nil

	case meetingSpeakerStatusMsg:
		m.applySpeakerStatus(msg)
		return m, nil

	case meetingStopRequestedMsg:
		m.markStopping()
		if m.liveSpeaker != nil {
			m.liveSpeaker.SetEnabled(false)
		}
		m.stickyLabel.Clear()
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

// markStopping freezes the recording clock and moves into the Stopping session
// phase. Idempotent: later stop keys / stop-phrase echoes do not advance stoppedAt.
func (m *meetingModel) markStopping() {
	if m.stoppedAt.IsZero() {
		m.stoppedAt = time.Now()
	}
	if m.sessionPhase == meetingSessionRecording {
		m.sessionPhase = meetingSessionStopping
	}
	m.quitting = true
	m.partial = ""
	if m.sessionPhase == meetingSessionStopping {
		m.status = "Stopping capture…"
		m.statusErr = false
	}
}

func (m *meetingModel) applySpeakerStatus(msg meetingSpeakerStatusMsg) {
	m.opts.SpeakerStatus = msg.status
	m.opts.SpeakerError = msg.detail
	// Only promote session chrome when capture has already stopped. Scripted
	// demos emit AnalysisRunning while STT is still live; treating that as
	// end-of-capture freezes the multi-speaker tape.
	if msg.status == meeting.AnalysisRunning && m.sessionPhase == meetingSessionStopping {
		m.sessionPhase = meetingSessionDiarizing
		m.quitting = true
		m.status = "Diarizing speakers…"
		m.statusErr = false
	}
}

// elapsed returns the duration to show in the header: frozen once stop is
// requested so diarization time does not look like live REC.
func (m meetingModel) elapsed() time.Duration {
	if !m.stoppedAt.IsZero() {
		return m.stoppedAt.Sub(m.started)
	}
	if m.started.IsZero() {
		return 0
	}
	return time.Since(m.started)
}

func (m meetingModel) requestStop() (meetingModel, tea.Cmd) {
	m.markStopping()
	if m.liveSpeaker != nil {
		m.liveSpeaker.SetEnabled(false)
	}
	m.stickyLabel.Clear()
	if m.opts.Cancel != nil {
		m.opts.Cancel()
	}
	if m.loopDone {
		return m, m.stopResultCmd()
	}
	return m, nil
}

// currentMeetingLiveLabel returns the live engine's last stable-ish label,
// sticky-held across brief empty gaps so consecutive turns from the same
// voice do not flash 🎤 mid-gap.
func (m *meetingModel) currentMeetingLiveLabel() string {
	if m.liveSpeaker == nil {
		return ""
	}
	stats := m.liveSpeaker.Stats()
	m.liveStats = stats
	m.liveStatsKnown = true
	if stats.Status != speaker.LiveHealthy && stats.Status != speaker.LiveRunning {
		return m.stickyLabel.Observe("", time.Now())
	}
	return m.stickyLabel.Observe(stats.LastLabel, time.Now())
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
	m.appendSystemLine(fmt.Sprintf("%s  %s %s",
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
	m.appendSystemLine(line)
	m.setFlash("★ moment marked important")
	return m, nil
}

func (m *meetingModel) setFlash(s string) {
	m.flash = s
	m.flashUntil = time.Now().Add(2 * time.Second)
}

func (m meetingModel) handleListenMsg(msg tea.Msg) (meetingModel, tea.Cmd) {
	switch msg := msg.(type) {
	case meetingStopRequestedMsg:
		m.markStopping()
	case meetingPhaseMsg:
		// After stop, ignore STT phase flips so "Listening" does not overwrite
		// Stopping/Diarizing chrome.
		if m.sessionPhase != meetingSessionRecording {
			return m, nil
		}
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
		if m.sessionPhase != meetingSessionRecording {
			return m, nil
		}
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
		if m.sessionPhase != meetingSessionRecording {
			return m, nil
		}
		m.partial = string(msg)
		if m.voiceMode == anim.ModeListening {
			m.voiceMode = anim.ModeHearing
		}
	case meetingUtteranceMsg:
		if m.sessionPhase != meetingSessionRecording {
			return m, nil
		}
		u := listen.Utterance(msg)
		m.utterances++
		m.partial = ""
		m.voiceMode = anim.ModeListening
		m.status = "Listening"
		// Demo providers may embed [speaker-N] in text; prefer that as the
		// structured label so the glyph is not a bare mic. Live engine (P2)
		// will set label via setUtteranceLabel without rewriting text.
		label, _ := splitSpeakerLabel(u.Text)
		if label == "" {
			label = m.currentMeetingLiveLabel()
		}
		m.lastUtteranceID = m.appendUtterance(u.At, label, u.Text)
	case meetingSpeakerLabelMsg:
		m.setUtteranceLabel(msg.lineID, msg.label)
	case meetingErrorMsg:
		if m.sessionPhase != meetingSessionRecording {
			return m, nil
		}
		m.errors++
		m.statusErr = true
		m.status = "Transcription error (retrying)"
		m.appendSystemLine(errorStyle.Render(fmt.Sprintf("  error: %v", msg.err)))
	case meetingSpeakerStatusMsg:
		m.applySpeakerStatus(msg)
	case meetingLoopDoneMsg:
		m.loopDone = true
		m.loopErr = msg.err
		m.voiceMode = anim.ModeIdle
		m.sessionPhase = meetingSessionDone
		if m.stoppedAt.IsZero() {
			m.stoppedAt = time.Now()
		}
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

func meetingAnalysisDetail(result meeting.AnalysisResult) string {
	if result.Error != "" {
		return result.Error
	}
	if result.Status == meeting.AnalysisComplete {
		noun := "speakers"
		if result.SpeakerCount == 1 {
			noun = "speaker"
		}
		detail := fmt.Sprintf("%d %s", result.SpeakerCount, noun)
		if result.Artifact != "" {
			detail += " · " + result.Artifact
		}
		return detail
	}
	return ""
}
