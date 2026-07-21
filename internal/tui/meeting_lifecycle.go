package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// stopMeetingRuntime cancels the listen loop, writes the dual-log trailer, and
// releases STT resources. Returns any Writer.Close failure so callers can
// surface a silent trailer/session_end write problem (files may already hold
// synced events). Idempotent when no runtime is active.
func (a *App) stopMeetingRuntime() error {
	_, err := a.stopMeetingRuntimeWithSummary()
	return err
}

// stopMeetingRuntimeWithSummary is stopMeetingRuntime that also returns the Close Summary.
func (a *App) stopMeetingRuntimeWithSummary() (meetinglog.Summary, error) {
	if a.meetingRT == nil {
		return meetinglog.Summary{}, nil
	}
	if a.meeting.opts.Cancel != nil {
		a.meeting.opts.Cancel()
	}
	var summary meetinglog.Summary
	var closeErr error
	if a.meetingRT.Writer != nil {
		s, err := a.meetingRT.Writer.Close()
		summary = s
		if err != nil {
			closeErr = fmt.Errorf("close meeting log: %w", err)
		}
	}
	if a.meetingRT.Cleanup != nil {
		a.meetingRT.Cleanup()
	}
	a.meetingRT = nil
	return summary, closeErr
}

// beginMeetingRoute opens the post-meeting picker (ask), auto-routes, or no-ops (off).
// Returns a tea.Cmd when auto-routing asynchronously; otherwise mutates screen and returns nil.
// Uses the live a.cfg (settings already mutate it via SetAndSave) so tests stay isolated
// from the developer's on-disk config.yaml.
//
// Per-session plan from the start-meeting picker (meetingRoutePlan) wins over
// global meeting.route.mode when set.
func (a *App) beginMeetingRoute(summary meetinglog.Summary) tea.Cmd {
	if summary.File == "" && summary.JSONLFile == "" {
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
		rcfg := meeting.WithDestination(routeCfg, plan.Dest)
		dest := plan.Dest
		body := rcfg.Body
		return func() tea.Msg {
			note, err := meeting.Render(summary, body)
			if err != nil {
				return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
			}
			router := meeting.NewDefaultRouter(rcfg)
			receipt, err := router.RouteMeeting(context.Background(), note, dest)
			return meetingRouteResultMsg{Banner: meeting.BannerLine(receipt), IsErr: err != nil}
		}
	case routePlanAsk:
		return a.openMeetingRoutePicker(summary, routeCfg)
	}

	// Fall back to configured global mode.
	switch routeCfg.Mode {
	case meeting.ModeOff:
		return nil
	case meeting.ModeAuto:
		if routeCfg.Default == "" {
			a.launcher = a.launcher.withBanner("Meeting route: mode=auto but no default destination", true)
			return nil
		}
		body := routeCfg.Body
		destID := routeCfg.Default
		rcfg := routeCfg
		return func() tea.Msg {
			note, err := meeting.Render(summary, body)
			if err != nil {
				return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
			}
			router := meeting.NewDefaultRouter(rcfg)
			receipt, err := router.RouteByID(context.Background(), note, destID)
			return meetingRouteResultMsg{Banner: meeting.BannerLine(receipt), IsErr: err != nil}
		}
	default: // ask
		return a.openMeetingRoutePicker(summary, routeCfg)
	}
}

// openMeetingRoutePicker discovers destinations (config + camp list) and shows
// the post-meeting chooser. No-ops when nothing is available.
func (a *App) openMeetingRoutePicker(summary meetinglog.Summary, routeCfg meeting.Config) tea.Cmd {
	router := meeting.NewDefaultRouter(routeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
	defer cancel()
	dests := router.DiscoverDestinations(ctx)
	if len(dests) == 0 {
		return nil
	}
	// Ensure discovered dests are routable via RouteByID if needed later.
	for _, d := range dests {
		routeCfg = meeting.WithDestination(routeCfg, d)
	}
	a.meetingRoute = newMeetingRoute(summary, routeCfg, dests)
	a.meetingRoute.width = a.width
	a.meetingRoute.height = a.height
	a.screen = screenMeetingRoute
	return nil
}
