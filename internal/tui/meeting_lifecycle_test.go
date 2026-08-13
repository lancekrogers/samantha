package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// collectMsgs executes a cmd tree (flattening batches) and returns every msg.
func collectMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

func recordedBundleWriter(t *testing.T) *meetinglog.Writer {
	t.Helper()
	w, err := meetinglog.CreateBundle(filepath.Join(t.TempDir(), "standup.meeting"), "Standup", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddNote("action item"); err != nil {
		t.Fatal(err)
	}
	return w
}

// Capture stop must fire the start-plan route and background diarization
// without gating the results screen on either (WI-162bbb R1/R2).
func TestMeetingDoneAutoRoutesDestPlanWithBackgroundDiarize(t *testing.T) {
	w := recordedBundleWriter(t)
	outDir := t.TempDir()
	released, cleaned, finalized := false, false, false
	rt := &MeetingRuntime{
		Writer:         w,
		ReleaseCapture: func() { released = true },
		Cleanup:        func() { cleaned = true },
		FinalizeSpeakers: func(context.Context) (meeting.AnalysisResult, error) {
			finalized = true
			// Diarize cancelled: the route must still be attempted.
			return meeting.AnalysisResult{Status: meeting.AnalysisError, Error: "speaker analysis cancelled"}, context.Canceled
		},
	}
	app := App{
		cfg:       &config.Config{},
		runCtx:    t.Context(),
		launcher:  newLauncher(&config.Config{}, nil, nil),
		meetingRT: rt,
		meetingRoutePlan: meetingRoutePlan{
			Kind: routePlanDest,
			Dest: meeting.Destination{ID: "docs", Type: meeting.TypeFile, Path: outDir},
		},
		width:  80,
		height: 24,
	}

	model, cmd := app.Update(meetingDoneMsg{})
	got := model.(App)
	if got.screen != screenMeetingResults {
		t.Fatalf("screen = %v, want results", got.screen)
	}
	if !released {
		t.Fatal("capture resources must release at stop")
	}
	if cleaned {
		t.Fatal("cleanup must wait for background diarize")
	}
	if !got.meetingRouteAuto || !strings.Contains(got.meetingResults.routeStatus, "Filing to docs") {
		t.Fatalf("auto route not armed: auto=%v status=%q", got.meetingRouteAuto, got.meetingResults.routeStatus)
	}
	if !got.meetingResults.analysisBusy || !strings.Contains(got.meetingResults.View(), "background") {
		t.Fatal("results must show background analysis status")
	}

	var routeMsg *meetingRouteResultMsg
	var analysisMsg *meetingAnalysisDoneMsg
	for _, msg := range collectMsgs(t, cmd) {
		switch m := msg.(type) {
		case meetingRouteResultMsg:
			routeMsg = &m
		case meetingAnalysisDoneMsg:
			analysisMsg = &m
		}
	}
	if routeMsg == nil || analysisMsg == nil {
		t.Fatalf("route=%v analysis=%v; both jobs must run", routeMsg, analysisMsg)
	}
	if routeMsg.IsErr {
		t.Fatalf("route failed: %s", routeMsg.Banner)
	}
	if !finalized || !cleaned {
		t.Fatalf("finalize=%v cleaned=%v after background job", finalized, cleaned)
	}
	exports, err := os.ReadDir(outDir)
	if err != nil || len(exports) != 1 {
		t.Fatalf("exported notes = %v (err %v), want 1", exports, err)
	}
	events, err := os.ReadFile(w.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"routed"`) {
		t.Fatal("missing routed provenance")
	}

	// Route result while reviewing: stay on results, show the outcome inline.
	model, _ = got.Update(*routeMsg)
	got = model.(App)
	if got.screen != screenMeetingResults || got.meetingResults.routeStatus != routeMsg.Banner {
		t.Fatalf("route result must land on results: screen=%v status=%q", got.screen, got.meetingResults.routeStatus)
	}
	// Diarize outcome folds into the review with its error surfaced.
	model, _ = got.Update(*analysisMsg)
	got = model.(App)
	if !strings.Contains(got.meetingResults.summary.SpeakerError, "cancelled") {
		t.Fatalf("analysis error not surfaced: %+v", got.meetingResults.summary)
	}
	// Leaving results carries the route outcome to the launcher; the consumed
	// plan must not re-route (no picker, no second export).
	model, cmd = got.Update(meetingResultsDoneMsg{summary: got.meetingResults.summary})
	got = model.(App)
	if got.screen != screenLauncher || cmd != nil {
		t.Fatalf("results exit: screen=%v cmd=%v", got.screen, cmd)
	}
	if got.launcher.banner != routeMsg.Banner {
		t.Fatalf("launcher banner = %q, want route outcome", got.launcher.banner)
	}
}

func TestMeetingReadyPersistsDestPlan(t *testing.T) {
	w := recordedBundleWriter(t)
	t.Cleanup(func() { _, _ = w.Close() })
	// beginRecording spawns the listen loop immediately; a cancelled runCtx
	// makes it exit before touching the nil capture/provider fakes.
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	app := App{
		cfg:    &config.Config{},
		runCtx: runCtx,
		meetingRoutePlan: meetingRoutePlan{
			Kind: routePlanDest,
			Dest: meeting.Destination{ID: "camp:obey-campaign", Type: meeting.TypeCampaign},
		},
		meeting: newEmbeddedMeeting(),
	}
	model, _ := app.Update(meetingReadyMsg{rt: &MeetingRuntime{Writer: w}})
	_ = model
	events, err := os.ReadFile(w.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"route_plan"`) ||
		!strings.Contains(string(events), "camp:obey-campaign") {
		t.Fatalf("route plan not persisted at meeting start:\n%s", events)
	}
}

// Ctrl+C on the results screen continues to routing instead of quitting the
// app — the exact key sequence that lost the field incident's route.
func TestResultsCtrlCContinuesInsteadOfQuit(t *testing.T) {
	app := App{
		cfg:            &config.Config{},
		screen:         screenMeetingResults,
		meetingResults: meetingResultsModel{summary: meetinglog.Summary{Description: "d"}},
	}
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := model.(App)
	if got.quitting {
		t.Fatal("ctrl+c on results must not quit the app")
	}
	if cmd == nil {
		t.Fatal("ctrl+c on results must continue")
	}
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(meetingResultsDoneMsg); !ok {
		t.Fatalf("continuation = %T, want meetingResultsDoneMsg", msgs[0])
	}
}

func TestRoutePickerCtrlCSkipsInsteadOfQuit(t *testing.T) {
	app := App{
		cfg:    &config.Config{},
		screen: screenMeetingRoute,
		meetingRoute: newMeetingRoute(
			meetinglog.Summary{Description: "d"},
			meeting.Config{Mode: meeting.ModeAsk},
			[]meeting.Destination{{ID: "docs", Type: meeting.TypeFile, Path: "/tmp"}},
		),
	}
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := model.(App)
	if got.quitting {
		t.Fatal("ctrl+c on picker must not quit the app")
	}
	msgs := collectMsgs(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	result, ok := msgs[0].(meetingRouteResultMsg)
	if !ok || !strings.Contains(strings.ToLower(result.Banner), "local") {
		t.Fatalf("picker ctrl+c = %+v, want explicit keep-local skip", msgs[0])
	}
}

func TestAnalysisDoneBannersOnLauncher(t *testing.T) {
	app := App{
		cfg:      &config.Config{},
		screen:   screenLauncher,
		launcher: newLauncher(&config.Config{}, nil, nil),
	}
	model, _ := app.Update(meetingAnalysisDoneMsg{
		result: meeting.AnalysisResult{Status: meeting.AnalysisComplete, SpeakerCount: 2},
	})
	got := model.(App)
	if !strings.Contains(got.launcher.banner, "Speaker labels updated") || got.launcher.bannerErr {
		t.Fatalf("banner = %q err=%v", got.launcher.banner, got.launcher.bannerErr)
	}
	model, _ = got.Update(meetingAnalysisDoneMsg{
		result: meeting.AnalysisResult{Status: meeting.AnalysisError, Error: "boom"},
	})
	got = model.(App)
	if !strings.Contains(got.launcher.banner, "boom") || !got.launcher.bannerErr {
		t.Fatalf("failure banner = %q err=%v", got.launcher.banner, got.launcher.bannerErr)
	}
}

func TestSweepBannerSummaries(t *testing.T) {
	if banner, _ := sweepBanner(nil); banner != "" {
		t.Fatalf("empty sweep banner = %q", banner)
	}
	banner, isErr := sweepBanner([]meeting.SweepResult{
		{Receipt: meeting.Receipt{Outcome: meeting.OutcomeRouted, DestinationID: "docs"}},
	})
	if !strings.Contains(banner, "Recovered") || isErr {
		t.Fatalf("delivered banner = %q err=%v", banner, isErr)
	}
	banner, isErr = sweepBanner([]meeting.SweepResult{{Err: os.ErrNotExist}})
	if banner == "" || !isErr {
		t.Fatalf("failure banner = %q err=%v", banner, isErr)
	}
}

// pumpMeetingLoop drives startLoop's cmd tree until the loop reports done.
func pumpMeetingLoop(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	timeout := time.After(5 * time.Second)
	done := make(chan []tea.Msg, 1)
	go func() {
		var msgs []tea.Msg
		queue := []tea.Cmd{cmd}
		for len(queue) > 0 {
			c := queue[0]
			queue = queue[1:]
			if c == nil {
				continue
			}
			switch m := c().(type) {
			case tea.BatchMsg:
				queue = append(queue, m...)
			case meetingChMsg:
				msgs = append(msgs, m.msg)
				if _, over := m.msg.(meetingLoopDoneMsg); over {
					done <- msgs
					return
				}
				queue = append(queue, waitMeetingCh(m.ch))
			case nil:
			default:
			}
		}
		done <- msgs
	}()
	select {
	case msgs := <-done:
		return msgs
	case <-timeout:
		t.Fatal("meeting loop pump timed out")
		return nil
	}
}

// Embedded stop must not run diarization inline; the CLI recorder keeps the
// synchronous, cancellable path.
func TestStartLoopDefersFinalizeOnlyWhenEmbedded(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		finalized := false
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // loop exits before touching capture/provider
		m := meetingModel{opts: MeetingOpts{
			Ctx:      ctx,
			Embedded: embedded,
			FinalizeSpeakers: func(context.Context) (meeting.AnalysisResult, error) {
				finalized = true
				return meeting.AnalysisResult{Status: meeting.AnalysisComplete}, nil
			},
		}}
		pumpMeetingLoop(t, m.startLoop())
		if finalized == embedded {
			t.Fatalf("embedded=%v finalized=%v: inline finalize must run only for the CLI path", embedded, finalized)
		}
	}
}
