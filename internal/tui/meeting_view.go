package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

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

// meetingLine is one viewport row. Utterances keep structured fields so a live
// speaker engine can attach/revise labels without string surgery.
type meetingLine struct {
	utterance bool
	id        int // stable id for utterance label updates (0 = non-utterance)
	at        time.Time
	label     string // speaker-N or empty → 🎤
	text      string // raw utterance text (may still include [speaker-N] prefix)
	rendered  string // preformatted system/note/error rows
}

// meetingSpeakerLabelMsg revises the glyph on an earlier utterance (async live ID).
type meetingSpeakerLabelMsg struct {
	lineID int
	label  string
}

func (m *meetingModel) appendSystemLine(line string) {
	m.appendMeetingLine(meetingLine{rendered: line})
}

func (m *meetingModel) appendUtterance(at time.Time, label, text string) int {
	id := m.utterances // already incremented by caller, or use len
	// Prefer a monotonic id independent of utterance count resets.
	id = 0
	for _, l := range m.lines {
		if l.utterance && l.id >= id {
			id = l.id + 1
		}
	}
	if id == 0 {
		id = 1
	}
	m.appendMeetingLine(meetingLine{
		utterance: true,
		id:        id,
		at:        at,
		label:     strings.TrimSpace(label),
		text:      text,
	})
	return id
}

func (m *meetingModel) appendMeetingLine(line meetingLine) {
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

// setUtteranceLabel updates a live line's speaker glyph by id and re-renders.
func (m *meetingModel) setUtteranceLabel(lineID int, label string) {
	if lineID <= 0 {
		return
	}
	label = strings.TrimSpace(label)
	for i := range m.lines {
		if m.lines[i].utterance && m.lines[i].id == lineID {
			m.lines[i].label = label
			m.refreshTranscript()
			return
		}
	}
}

func (l meetingLine) view() string {
	if !l.utterance {
		return l.rendered
	}
	return formatMeetingUtteranceLine(l.at, l.label, l.text)
}

// formatMeetingUtteranceLine builds "HH:MM:SS  <glyph>  text".
// Empty label → mic glyph. Non-empty → colored speaker-N (chat palette).
// Bracket prefixes in text ([speaker-N]) are stripped from the body so the id
// is not shown twice when the label glyph is set.
func formatMeetingUtteranceLine(at time.Time, label, text string) string {
	fromText, spoken := splitSpeakerLabel(text)
	if label == "" {
		label = fromText
	}
	if fromText == "" {
		spoken = text
	}
	glyph := headerStyle.Render("🎤")
	if label != "" {
		glyph = speakerLabelStyle(label).Render(label)
	}
	when := at
	if when.IsZero() {
		when = time.Now()
	}
	return fmt.Sprintf("%s  %s %s",
		dimStyle.Render(when.Format("15:04:05")),
		glyph,
		normalStyle.Render(spoken),
	)
}

func (m *meetingModel) refreshTranscript() {
	if !m.ready {
		return
	}
	parts := make([]string, 0, len(m.lines))
	for _, l := range m.lines {
		parts = append(parts, l.view())
	}
	content := strings.Join(parts, "\n")
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

func (m meetingModel) View() string {
	if !m.ready {
		return "\n  " + headerStyle.Render("Starting meeting recorder…") + "\n"
	}
	w := max(m.width, 1)
	styles := voiceAnimStyles()

	// Live REC only while capture is active. After stop, show honest phase so
	// diarization time never looks like the meeting is still recording.
	rec := errorStyle.Bold(true).Render("● REC")
	switch m.sessionPhase {
	case meetingSessionStopping:
		rec = warningStyle.Bold(true).Render("Stopping")
	case meetingSessionDiarizing:
		rec = statusStyle.Bold(true).Render("Diarizing")
	case meetingSessionDone:
		rec = dimStyle.Bold(true).Render("Stopped")
	}
	elapsed := formatMeetingDuration(m.elapsed().Round(time.Second))
	header := fmt.Sprintf("%s  %s  %s  %s",
		headerStyle.Render("Meeting"),
		normalStyle.Render(m.opts.Description),
		rec,
		dimStyle.Render(elapsed),
	)
	header = ansi.Truncate(header, w, "…")

	// Present one meeting-level artifact. Machine sidecars live inside the
	// bundle and should not compete for attention in the recording UI.
	pathLine := dimStyle.Render(ansi.Truncate("  Bundle: "+m.opts.Path, w, "…"))
	speakerLine := dimStyle.Render(ansi.Truncate("  "+meetingSpeakerStatus(m.opts.SpeakerStatus, m.opts.SpeakerError), w, "…"))
	if m.liveStatsKnown {
		speakerLine += "\n" + dimStyle.Render(ansi.Truncate("  live "+liveSpeakerFooterLabel(m.liveStats), w, "…"))
	}
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

	return header + "\n" + pathLine + "\n" + speakerLine + "\n" + rule + "\n" +
		stage + partial +
		m.viewport.View() + "\n" +
		rule + "\n" +
		actionBar + "\n" +
		noteBox + "\n" +
		footer
}

func meetingSpeakerStatus(status meeting.AnalysisStatus, detail string) string {
	if status == "" {
		status = meeting.AnalysisDisabled
	}
	line := "Speaker analysis: " + string(status)
	if detail != "" {
		line += " — " + detail
	}
	if status == meeting.AnalysisDisabled {
		line += " (recording unaffected)"
	}
	return line
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
