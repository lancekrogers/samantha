package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/meetinglog"
	"github.com/lancekrogers/samantha/internal/meetingroute"
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
func (a *App) beginMeetingRoute(summary meetinglog.Summary) tea.Cmd {
	if summary.File == "" && summary.JSONLFile == "" {
		return nil
	}
	cfg := a.cfg
	if cfg == nil {
		return nil
	}
	routeCfg := meetingroute.FromConfig(cfg)
	switch routeCfg.Mode {
	case meetingroute.ModeOff:
		return nil
	case meetingroute.ModeAuto:
		if routeCfg.Default == "" {
			a.launcher = a.launcher.withBanner("Meeting route: mode=auto but no default destination", true)
			return nil
		}
		body := routeCfg.Body
		destID := routeCfg.Default
		rcfg := routeCfg
		return func() tea.Msg {
			note, err := meetingroute.Render(summary, body)
			if err != nil {
				return meetingRouteResultMsg{Banner: "Meeting route failed (notes kept local): " + err.Error(), IsErr: true}
			}
			router := meetingroute.NewDefaultRouter(rcfg)
			receipt, err := router.RouteByID(context.Background(), note, destID)
			return meetingRouteResultMsg{Banner: meetingroute.BannerLine(receipt), IsErr: err != nil}
		}
	default: // ask
		router := meetingroute.NewDefaultRouter(routeCfg)
		dests := router.AvailableDestinations()
		if len(dests) == 0 {
			// Nothing to pick — stay on launcher without blocking.
			return nil
		}
		a.meetingRoute = newMeetingRoute(summary, routeCfg, dests)
		a.meetingRoute.width = a.width
		a.meetingRoute.height = a.height
		a.screen = screenMeetingRoute
		return nil
	}
}
