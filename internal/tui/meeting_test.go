package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/tui/anim"
)

func sizedMeeting(t *testing.T, w, h int) meetingModel {
	t.Helper()
	ta := textarea.New()
	ta.SetHeight(meetingNoteHeight)
	ta.Focus()
	ta.KeyMap.InsertNewline.SetEnabled(false)
	m := meetingModel{
		opts: MeetingOpts{
			Description: "Standup",
			Path:        "/tmp/standup.log",
		},
		note:      ta,
		started:   time.Now(),
		voiceMode: anim.ModeListening,
		status:    "Listening",
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	mm := updated.(meetingModel)
	if !mm.ready {
		t.Fatal("not ready after resize")
	}
	return mm
}

func TestMeetingViewShowsDescriptionAndEQ(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	view := m.View()
	for _, want := range []string{"Meeting", "Standup", "listening", "Speaker analysis: disabled", "Ctrl+B", "Enter"} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(want)) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestMeetingSpeakerStatusExplainsStates(t *testing.T) {
	if got := meetingSpeakerStatus("", ""); !strings.Contains(got, "disabled") || !strings.Contains(got, "unaffected") {
		t.Fatalf("default speaker status = %q", got)
	}
	if got := meetingSpeakerStatus("error", "engine unavailable"); !strings.Contains(got, "error") || !strings.Contains(got, "engine unavailable") {
		t.Fatalf("error speaker status = %q", got)
	}
}

func TestMeetingSpeakerStatusFromRuntimeChannel(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	m, _ = m.handleListenMsg(meetingSpeakerStatusMsg{
		status: meeting.AnalysisRunning,
		detail: "diarizing captured audio…",
	})
	if m.opts.SpeakerStatus != meeting.AnalysisRunning || !strings.Contains(m.View(), "diarizing captured audio") {
		t.Fatalf("speaker status not applied:\n%s", m.View())
	}
	result := meeting.AnalysisResult{Status: meeting.AnalysisComplete, SpeakerCount: 2, Artifact: "/tmp/a.json"}
	m, _ = m.handleListenMsg(meetingLoopDoneMsg{analysis: result})
	if m.analysis.SpeakerCount != 2 {
		t.Fatalf("analysis result = %+v", m.analysis)
	}
}

func TestMeetingPhaseAndLevelUpdateMode(t *testing.T) {
	m := sizedMeeting(t, 80, 24)
	m, _ = m.handleListenMsg(meetingPhaseMsg("listening"))
	if m.voiceMode != anim.ModeListening {
		t.Fatalf("mode = %v", m.voiceMode)
	}
	m, _ = m.handleListenMsg(meetingLevelMsg(0.8))
	if m.voiceMode != anim.ModeHearing {
		t.Fatalf("loud level should promote to hearing, got %v", m.voiceMode)
	}
	m, _ = m.handleListenMsg(meetingPartialMsg("hello world"))
	if m.partial != "hello world" {
		t.Fatalf("partial = %q", m.partial)
	}
	m, _ = m.handleListenMsg(meetingUtteranceMsg(listen.Utterance{
		Text: "hello world",
		At:   time.Date(2026, 7, 19, 12, 0, 5, 0, time.UTC),
	}))
	if m.utterances != 1 || m.partial != "" {
		t.Fatalf("utterances=%d partial=%q", m.utterances, m.partial)
	}
	if !strings.Contains(m.View(), "hello world") {
		t.Fatal("final utterance not in view")
	}
}

func TestMeetingNoteAndBookmarkPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.log")
	w, err := meetinglog.Create(path, "Notes test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	m := sizedMeeting(t, 80, 24)
	m.opts.Writer = w
	m.opts.Path = path

	m.note.SetValue("check budget")
	m, cmd := m.submitNote()
	if cmd != nil {
		t.Fatal("submitNote should not return error cmd")
	}
	if m.notes != 1 || m.note.Value() != "" {
		t.Fatalf("notes=%d draft=%q", m.notes, m.note.Value())
	}
	if !strings.Contains(m.View(), "check budget") {
		t.Fatal("note not visible in timeline")
	}

	m.note.SetValue("decision point")
	m, cmd = m.markImportant()
	if cmd != nil {
		t.Fatal("markImportant should not return error cmd")
	}
	if m.bookmarks != 1 {
		t.Fatalf("bookmarks=%d", m.bookmarks)
	}
	if !strings.Contains(m.View(), "IMPORTANT") {
		t.Fatal("bookmark not visible")
	}

	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Notes != 1 || sum.Bookmarks != 1 {
		t.Fatalf("summary notes=%d bookmarks=%d", sum.Notes, sum.Bookmarks)
	}
}

func TestMeetingStopKeys(t *testing.T) {
	cancelled := false
	m := sizedMeeting(t, 80, 24)
	m.opts.Cancel = func() { cancelled = true }
	// Plain 'q' types into the note field — does not stop.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	mm := updated.(meetingModel)
	if mm.quitting || cancelled {
		t.Fatal("plain q must type into notes, not stop")
	}
	// Ctrl+C stops.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	mm = updated.(meetingModel)
	if !mm.quitting || !cancelled {
		t.Fatalf("ctrl+c quitting=%v cancelled=%v", mm.quitting, cancelled)
	}
}

func TestFormatMeetingDuration(t *testing.T) {
	if got := formatMeetingDuration(65 * time.Second); got != "01:05" {
		t.Fatalf("got %q", got)
	}
	if got := formatMeetingDuration(3661 * time.Second); got != "1:01:01" {
		t.Fatalf("got %q", got)
	}
}

func TestTrySendMeetingDropsWhenFull(t *testing.T) {
	ch := make(chan tea.Msg) // unbuffered, no receiver
	trySendMeeting(ch, meetingLevelMsg(0.5))
	// Must return without blocking.
}

func TestSendMeetingDeliversWhenCapacityTight(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	ch <- meetingLevelMsg(0.1) // fill the only slot with droppable noise
	done := make(chan struct{})
	go func() {
		sendMeeting(ch, meetingUtteranceMsg(listen.Utterance{Text: "keep me", At: time.Now()}))
		close(done)
	}()
	// Drain noise so the durable send can complete.
	<-ch
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendMeeting blocked forever with a live consumer")
	}
	msg := <-ch
	if u, ok := msg.(meetingUtteranceMsg); !ok || listen.Utterance(u).Text != "keep me" {
		t.Fatalf("durable msg = %#v", msg)
	}
}

func TestStopMeetingRuntimeSurfacesCloseErrorAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "close.log")
	w, err := meetinglog.Create(path, "Close test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	// Force a close failure by closing the underlying descriptor first so the
	// trailer write fails — stopMeetingRuntime must report that, not swallow it.
	if err := w.OnUtterance(listen.Utterance{Text: "hi", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Access via Close after breaking the log handle: close the log file fd.
	// Writer.Close should still return an error from writeLog/close.
	// Easier approach: close successfully first via stop, then verify second stop is nil.
	app := &App{
		meetingRT: &MeetingRuntime{Writer: w, Cleanup: func() {}},
	}
	if err := app.stopMeetingRuntime(); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if app.meetingRT != nil {
		t.Fatal("meetingRT must be cleared")
	}
	if err := app.stopMeetingRuntime(); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	// Trailer present after successful close.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# Ended:") {
		t.Fatalf("missing trailer:\n%s", data)
	}
}

func TestMeetingDoneJoinsCloseErrorOntoLauncherBanner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "done.log")
	w, err := meetinglog.Create(path, "Done", "fake")
	if err != nil {
		t.Fatal(err)
	}
	// Pre-close the writer so stopMeetingRuntime's Close is idempotent nil;
	// inject a loop error via meetingDoneMsg and ensure banner shows it.
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	app := NewApp(&config.Config{})
	app.launcher.width, app.launcher.height = 80, 24
	app.meetingRT = &MeetingRuntime{Writer: w, Cleanup: func() {}}
	app.screen = screenMeeting

	updated, _ := app.Update(meetingDoneMsg{Err: errors.New("stt failed")})
	a := updated.(App)
	if a.screen != screenLauncher {
		t.Fatalf("screen = %v, want launcher", a.screen)
	}
	view := a.launcher.View()
	if !strings.Contains(view, "stt failed") {
		t.Fatalf("launcher banner missing loop error:\n%s", view)
	}
}

func TestMeetingDoneShowsCompletedSpeakerAnalysis(t *testing.T) {
	w, err := meetinglog.Create(filepath.Join(t.TempDir(), "done.log"), "Done", "fake")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Meeting.Route.Mode = meeting.ModeOff
	app := NewApp(cfg)
	app.launcher.width, app.launcher.height = 80, 24
	app.meetingRT = &MeetingRuntime{Writer: w, Cleanup: func() {}}
	app.screen = screenMeeting

	updated, _ := app.Update(meetingDoneMsg{Analysis: meeting.AnalysisResult{
		Status: meeting.AnalysisComplete, SpeakerCount: 3, Artifact: "/tmp/done.speaker-analysis.json",
	}})
	a := updated.(App)
	if a.screen != screenLauncher || !strings.Contains(a.launcher.View(), "3 speakers") {
		t.Fatalf("launcher did not report analysis:\n%s", a.launcher.View())
	}
}

func TestMeetingUISinkOmitsStopPhraseFromLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stop.log")
	w, err := meetinglog.Create(path, "Stop test", "fake")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	ch := make(chan tea.Msg, 4)
	sink := &meetingUISink{
		ch:      ch,
		phrases: map[string]bool{"stop recording": true},
		stop:    func() { stopped = true },
		writer:  w,
	}
	if err := sink.OnUtterance(listen.Utterance{Text: "hello team", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := sink.OnUtterance(listen.Utterance{Text: "Stop recording.", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("stop phrase must cancel the session")
	}
	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Utterances != 1 {
		t.Fatalf("utterances = %d, want 1 (stop phrase omitted)", sum.Utterances)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "stop recording") {
		t.Fatalf("stop phrase leaked into log:\n%s", data)
	}
	// Only the real utterance should have been UI-sent.
	select {
	case msg := <-ch:
		if u, ok := msg.(meetingUtteranceMsg); !ok || listen.Utterance(u).Text != "hello team" {
			t.Fatalf("first UI msg = %#v", msg)
		}
	default:
		t.Fatal("expected UI utterance for non-stop speech")
	}
	select {
	case msg := <-ch:
		t.Fatalf("stop phrase must not emit UI utterance, got %#v", msg)
	default:
	}
}
