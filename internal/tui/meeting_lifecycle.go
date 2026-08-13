package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// stopMeetingRuntime cancels the listen loop, writes the bundle trailer, and
// releases STT resources. Returns any Writer.Close failure so callers can
// surface a silent trailer/session_end write problem (files may already hold
// synced events). Idempotent when no runtime is active.
func (a *App) stopMeetingRuntime() error {
	rt, _, err := a.finishMeetingCapture()
	if rt != nil && rt.Cleanup != nil {
		rt.Cleanup()
	}
	return err
}

// finishMeetingCapture ends capture: cancels the listen loop, writes the
// bundle trailer / session_end, and releases mic/STT resources. The returned
// runtime still owns the speaker analyzer so background diarization can run;
// callers must ensure rt.Cleanup runs, directly or after diarize.
func (a *App) finishMeetingCapture() (*MeetingRuntime, meetinglog.Summary, error) {
	rt := a.meetingRT
	if rt == nil {
		return nil, meetinglog.Summary{}, nil
	}
	if a.meeting.opts.Cancel != nil {
		a.meeting.opts.Cancel()
	}
	var summary meetinglog.Summary
	var closeErr error
	if rt.Writer != nil {
		s, err := rt.Writer.Close()
		summary = s
		if err != nil {
			closeErr = fmt.Errorf("close meeting log: %w", err)
		}
	}
	if rt.ReleaseCapture != nil {
		rt.ReleaseCapture()
	}
	a.meetingRT = nil
	return rt, summary, closeErr
}

// meetingAnalysisDoneMsg delivers background diarization completion.
type meetingAnalysisDoneMsg struct {
	result meeting.AnalysisResult
	err    error
}

// runBackgroundDiarize finalizes speaker analysis off the UI thread, then
// releases the analyzer. The bundle writer is already closed; the analysis
// appends reopen the bundle files (additive enrichment). Tied to the app
// context: quitting cancels enrichment, never the saved transcript (R4).
func runBackgroundDiarize(ctx context.Context, rt *MeetingRuntime) tea.Cmd {
	finalize, cleanup := rt.FinalizeSpeakers, rt.Cleanup
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		actx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		defer cancel()
		result, err := finalize(actx)
		if cleanup != nil {
			cleanup()
		}
		return meetingAnalysisDoneMsg{result: result, err: err}
	}
}

// analysisFailure reports whether a diarization outcome needs an error
// surface, with its human detail.
func analysisFailure(result meeting.AnalysisResult, err error) (bool, string) {
	if result.Status == meeting.AnalysisComplete && err == nil {
		return false, ""
	}
	if result.Error != "" {
		return true, result.Error
	}
	if err != nil {
		return true, err.Error()
	}
	return true, string(result.Status)
}

// meetingSweepDoneMsg reports startup retries of undelivered route plans.
type meetingSweepDoneMsg struct{ results []meeting.SweepResult }

// sweepMeetingRoutesCmd retries route plans left undelivered by a previous
// session (quit, crash, or failure between capture end and route completion).
func sweepMeetingRoutesCmd(cfg *config.Config) tea.Cmd {
	if cfg == nil {
		return nil
	}
	routeCfg := meeting.FromConfig(cfg)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		router := meeting.NewDefaultRouter(routeCfg)
		return meetingSweepDoneMsg{results: meeting.SweepPendingRoutes(ctx, router, config.MeetingsDir())}
	}
}

// sweepBanner condenses sweep results into one launcher line.
func sweepBanner(results []meeting.SweepResult) (string, bool) {
	var delivered, failed int
	detail := ""
	for _, r := range results {
		if r.Err != nil {
			failed++
			detail = r.Err.Error()
			continue
		}
		delivered++
		detail = meeting.BannerLine(r.Receipt)
	}
	switch {
	case delivered == 0 && failed == 0:
		return "", false
	case failed > 0:
		return fmt.Sprintf("Meeting route retry: %d delivered, %d failed (%s)", delivered, failed, detail), true
	case delivered == 1:
		return "Recovered pending meeting route — " + detail, false
	default:
		return fmt.Sprintf("Recovered %d pending meeting routes", delivered), false
	}
}

// beginMeetingRoute opens the post-meeting picker (ask), auto-routes, or no-ops (off).
// Returns a tea.Cmd when discovery or routing runs asynchronously; otherwise mutates
// screen/banner and returns nil.
// Uses the live a.cfg (settings already mutate it via SetAndSave) so tests stay isolated
// from the developer's on-disk config.yaml.
//
// Per-session plan from the start-meeting picker (meetingRoutePlan) wins over
// global meeting.route.mode when set.
func (a *App) beginMeetingRoute(summary meetinglog.Summary) tea.Cmd {
	if summary.Bundle == "" || summary.JSONLFile == "" {
		return nil
	}
	cfg := a.cfg
	if cfg == nil {
		return nil
	}
	routeCfg := meeting.FromConfig(cfg)
	plan := a.meetingRoutePlan
	a.meetingRoutePlan = meetingRoutePlan{} // consume once

	// Explicit choice at meeting start.
	switch plan.Kind {
	case routePlanLocal:
		a.launcher = a.launcher.withBanner(meeting.BannerLine(meeting.Receipt{Outcome: meeting.OutcomeSkipped}), false)
		return nil
	case routePlanDest:
		if plan.Dest.ID == "" {
			break
		}
		return destRouteCmd(summary, routeCfg, plan.Dest)
	case routePlanAsk:
		return a.openMeetingRoutePicker(summary, routeCfg)
	}

	// Fall back to configured global mode.
	switch routeCfg.Mode {
	case meeting.ModeOff:
		return nil
	case meeting.ModeAuto:
		return a.autoRouteMeeting(summary, routeCfg)
	default: // ask
		return a.openMeetingRoutePicker(summary, routeCfg)
	}
}

// destRouteCmd renders and routes to a start-plan destination. The router
// appends durable routed / route_failed provenance to the bundle; a render
// failure appends route_failed here so sweep retries stay bounded.
func destRouteCmd(summary meetinglog.Summary, routeCfg meeting.Config, dest meeting.Destination) tea.Cmd {
	rcfg := meeting.WithDestination(routeCfg, dest)
	body := rcfg.Body
	return func() tea.Msg {
		note, err := meeting.Render(summary, body)
		if err != nil {
			_ = meeting.AppendRouteFailedEvent(summary.JSONLFile, meeting.Receipt{
				DestinationID: dest.ID, Type: dest.Type,
				Outcome: meeting.OutcomeFailed, Detail: err.Error(),
			})
			return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
		}
		router := meeting.NewDefaultRouter(rcfg)
		receipt, err := router.RouteMeeting(context.Background(), note, dest)
		return meetingRouteResultMsg{Banner: meeting.BannerLine(receipt), IsErr: err != nil}
	}
}

// openMeetingRoutePicker returns a tea.Cmd that discovers destinations off the
// Update goroutine, then delivers meetingRouteReadyMsg to open the picker.
func (a *App) openMeetingRoutePicker(summary meetinglog.Summary, routeCfg meeting.Config) tea.Cmd {
	a.launcher = a.launcher.withBanner("Discovering destinations…", false)
	a.screen = screenLauncher
	return func() tea.Msg {
		router := meeting.NewDefaultRouter(routeCfg)
		ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
		defer cancel()
		expanded, dests, err := router.ExpandForRouting(ctx)
		return meetingRouteReadyMsg{
			summary:  summary,
			routeCfg: expanded,
			dests:    dests,
			err:      err,
		}
	}
}

// meetingRouteReadyMsg is the async result of post-meeting destination discovery.
type meetingRouteReadyMsg struct {
	summary  meetinglog.Summary
	routeCfg meeting.Config
	dests    []meeting.Destination
	err      error
}

// applyMeetingRouteReady opens the picker or falls back to the launcher.
func (a *App) applyMeetingRouteReady(msg meetingRouteReadyMsg) {
	if len(msg.dests) == 0 {
		banner := "Meeting notes kept local only (no destinations available)"
		if msg.err != nil {
			banner = "Meeting notes kept local only (destination discovery failed)"
		}
		a.launcher = a.launcher.withBanner(banner, msg.err != nil)
		a.screen = screenLauncher
		return
	}
	routeCfg := msg.routeCfg
	for _, d := range msg.dests {
		routeCfg = meeting.WithDestination(routeCfg, d)
	}
	a.meetingRoute = newMeetingRoute(msg.summary, routeCfg, msg.dests)
	a.meetingRoute.width = a.width
	a.meetingRoute.height = a.height
	a.screen = screenMeetingRoute
}

// autoRouteMeeting discovers destinations (so camp: defaults resolve) then routes.
func (a *App) autoRouteMeeting(summary meetinglog.Summary, routeCfg meeting.Config) tea.Cmd {
	if routeCfg.Default == "" {
		a.launcher = a.launcher.withBanner("Meeting route: mode=auto but no default destination", true)
		return nil
	}
	a.launcher = a.launcher.withBanner("Routing meeting notes…", false)
	a.screen = screenLauncher
	body := routeCfg.Body
	destID := routeCfg.Default
	base := routeCfg
	return func() tea.Msg {
		router := meeting.NewDefaultRouter(base)
		ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
		defer cancel()
		expanded, dests, discoverErr := router.ExpandForRouting(ctx)
		dest, ok := meeting.ResolveDestination(expanded, destID, dests)
		if !ok {
			detail := fmt.Sprintf("unknown destination %q", destID)
			if discoverErr != nil {
				detail = fmt.Sprintf("%s (camp list: %v)", detail, discoverErr)
			}
			return meetingRouteResultMsg{
				Banner: "Meeting route failed (notes kept local): " + detail,
				IsErr:  true,
			}
		}
		note, err := meeting.Render(summary, body)
		if err != nil {
			return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
		}
		router = meeting.NewDefaultRouter(meeting.WithDestination(expanded, dest))
		receipt, err := router.RouteMeeting(context.Background(), note, dest)
		return meetingRouteResultMsg{Banner: meeting.BannerLine(receipt), IsErr: err != nil}
	}
}
