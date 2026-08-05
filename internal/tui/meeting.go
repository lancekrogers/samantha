package tui

import (
	"context"
	"errors"
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
	Ctx              context.Context
	Cancel           context.CancelFunc
	Capture          listen.Resetter
	Provider         stt.Provider
	Writer           *meetinglog.Writer
	Description      string
	Path             string // .meeting bundle path
	StopPhrases      map[string]bool
	SpeakerStatus    meeting.AnalysisStatus
	SpeakerError     string
	FinalizeSpeakers func(context.Context) (meeting.AnalysisResult, error)
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
type meetingLoopDoneMsg struct {
	err      error
	analysis meeting.AnalysisResult
}
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

	started      time.Time
	stoppedAt    time.Time // zero while still recording; freezes elapsed when set
	sessionPhase meetingSessionPhase
	// analysisCancel aborts FinalizeSpeakers when the user abandons mid-diarize.
	// Set by the listen-loop goroutine; cleared when the loop finishes.
	analysisCancel context.CancelFunc
	liveSpeaker    LiveSpeakerController
	liveStats      speaker.LiveStats
	liveStatsKnown bool
	// lastUtteranceID is the newest live speech row for async label attachment.
	lastUtteranceID int
	utterances      int
	notes           int
	bookmarks       int
	errors          int
	quitting        bool
	loopDone        bool
	loopErr         error
	analysis        meeting.AnalysisResult
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
	// The recording context is canceled to stop capture. Keep Bubble Tea alive
	// until the loop has finalized post-capture speaker analysis and delivered
	// its terminal status; meetingLoopDoneMsg then exits the program normally.
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(meetingModel); ok && fm.loopErr != nil {
		return fm.loopErr
	}
	if opts.Writer != nil {
		summary, closeErr := opts.Writer.Close()
		if closeErr != nil {
			return closeErr
		}
		results := newMeetingResults(summary)
		results.standalone = true
		resultsProgram := tea.NewProgram(standaloneMeetingResults{meetingResultsModel: results}, tea.WithAltScreen())
		if _, err := resultsProgram.Run(); err != nil {
			return err
		}
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
	m.analysis = meeting.AnalysisResult{}
	m.partial = ""
	m.stoppedAt = time.Time{}
	m.sessionPhase = meetingSessionRecording
	m.liveSpeaker = opts.LiveSpeaker
	m.liveStatsKnown = false
	m.lastUtteranceID = 0
	if m.opts.SpeakerStatus == "" {
		m.opts.SpeakerStatus = meeting.AnalysisDisabled
	}
	cmds := []tea.Cmd{m.startLoop(), meetingTickCmd(), textarea.Blink}
	if m.liveSpeaker != nil {
		m.liveSpeaker.SetEnabled(true)
		cmds = append(cmds, liveSpeakerStatsCmd(m.liveSpeaker))
	}
	return tea.Batch(cmds...)
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

// meetingAnalysisCancelMsg registers the cancel func for mid-diarize abandon.
type meetingAnalysisCancelMsg struct{ cancel context.CancelFunc }

// meetingAnalysisCancelClearMsg drops the cancel handle after Finalize returns.
type meetingAnalysisCancelClearMsg struct{}

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
		var analysis meeting.AnalysisResult
		if opts.FinalizeSpeakers != nil {
			analysis = runMeetingFinalize(ch, opts.FinalizeSpeakers)
		}
		// Loop completion must not be dropped: UI uses it to exit cleanly.
		sendMeeting(ch, meetingLoopDoneMsg{err: err, analysis: analysis})
	}()

	return tea.Batch(waitMeetingCh(ch), demoMeetingSpeakerStatusCmds())
}

// runMeetingFinalize runs offline speaker analysis without blocking UI abandon.
// Native sherpa Process is not interruptible mid-call; we still cancel the
// context and open results immediately when the user abandons, while a
// background Finalize may finish later (artifacts optional).
func runMeetingFinalize(ch chan<- tea.Msg, finalize func(context.Context) (meeting.AnalysisResult, error)) meeting.AnalysisResult {
	sendMeeting(ch, meetingSpeakerStatusMsg{status: meeting.AnalysisRunning, detail: "diarizing captured audio…"})
	analysisCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	sendMeeting(ch, meetingAnalysisCancelMsg{cancel: cancel})

	type finalizeResult struct {
		analysis meeting.AnalysisResult
		err      error
	}
	done := make(chan finalizeResult, 1)
	go func() {
		a, e := finalize(analysisCtx)
		done <- finalizeResult{analysis: a, err: e}
	}()

	var analysis meeting.AnalysisResult
	var analysisErr error
	abandoned := false
	select {
	case r := <-done:
		analysis, analysisErr = r.analysis, r.err
	case <-analysisCtx.Done():
		// Abandon: do not wait for multi-minute native Process.
		abandoned = true
		analysis = meeting.AnalysisResult{
			Status: meeting.AnalysisError,
			Error:  "speaker analysis cancelled",
		}
		analysisErr = analysisCtx.Err()
		go func() { <-done }() // drain when native work eventually returns
	}
	cancel()
	sendMeeting(ch, meetingAnalysisCancelClearMsg{})
	// Normalize before/without relying on analysisCtx after cancel() — cancel
	// always sets ctx.Err() even when Finalize already completed successfully.
	analysis = normalizeMeetingAnalysisResult(analysis, analysisErr, abandoned)
	sendMeeting(ch, meetingSpeakerStatusMsg{status: analysis.Status, detail: meetingAnalysisDetail(analysis)})
	return analysis
}

// normalizeMeetingAnalysisResult maps cancel/errors into a stable UI result.
// SpeakerSession often returns (result, nil) with Error filled in — check the
// Go error and result text, not only analysisErr != nil.
func normalizeMeetingAnalysisResult(analysis meeting.AnalysisResult, analysisErr error, abandoned bool) meeting.AnalysisResult {
	if analysisErr != nil && analysis.Error == "" {
		analysis.Error = analysisErr.Error()
	}
	cancelled := abandoned ||
		errors.Is(analysisErr, context.Canceled) ||
		strings.Contains(strings.ToLower(analysis.Error), "cancel")
	if cancelled {
		analysis.Status = meeting.AnalysisError
		analysis.Error = "speaker analysis cancelled"
		return analysis
	}
	if analysisErr != nil {
		if analysis.Status == "" || analysis.Status == meeting.AnalysisRunning || analysis.Status == meeting.AnalysisComplete {
			analysis.Status = meeting.AnalysisError
		}
		if analysis.Error == "" {
			analysis.Error = analysisErr.Error()
		}
	}
	return analysis
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
		// Attach/refresh the latest speech row when the live engine names a voice.
		if m.lastUtteranceID > 0 && msg.stats.LastLabel != "" &&
			(msg.stats.Status == speaker.LiveHealthy || msg.stats.Status == speaker.LiveRunning) {
			m.setUtteranceLabel(m.lastUtteranceID, msg.stats.LastLabel)
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
		return m, nil

	case meetingAnalysisCancelMsg:
		m.analysisCancel = msg.cancel
		return m, nil

	case meetingAnalysisCancelClearMsg:
		m.analysisCancel = nil
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
	// Second stop (or stop during diarize) abandons offline analysis so the
	// user is not stuck waiting on multi-minute Finalize.
	if m.sessionPhase == meetingSessionDiarizing && m.analysisCancel != nil {
		m.analysisCancel()
		m.analysisCancel = nil
		m.status = "Cancelling speaker analysis…"
		m.statusErr = false
	}
	m.markStopping()
	if m.liveSpeaker != nil {
		m.liveSpeaker.SetEnabled(false)
	}
	if m.opts.Cancel != nil {
		m.opts.Cancel()
	}
	if m.loopDone {
		return m, m.stopResultCmd()
	}
	return m, nil
}

// currentMeetingLiveLabel returns the live engine's last stable-ish label.
func (m *meetingModel) currentMeetingLiveLabel() string {
	if m.liveSpeaker == nil {
		return ""
	}
	stats := m.liveSpeaker.Stats()
	m.liveStats = stats
	m.liveStatsKnown = true
	if stats.LastLabel == "" {
		return ""
	}
	if stats.Status != speaker.LiveHealthy && stats.Status != speaker.LiveRunning {
		return ""
	}
	return stats.LastLabel
}

// stopResultCmd leaves the recorder: embedded → launcher; standalone → Quit.
func (m meetingModel) stopResultCmd() tea.Cmd {
	if m.opts.Embedded {
		err := m.loopErr
		analysis := m.analysis
		return func() tea.Msg { return meetingDoneMsg{Err: err, Analysis: analysis} }
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
	case meetingAnalysisCancelMsg:
		m.analysisCancel = msg.cancel
	case meetingAnalysisCancelClearMsg:
		m.analysisCancel = nil
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
		m.analysis = msg.analysis
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
