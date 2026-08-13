package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

const (
	// SweepWindow bounds how far back the startup sweep looks for undelivered
	// route plans. Older bundles stay recoverable via `samantha meeting route`.
	SweepWindow = 14 * 24 * time.Hour
	// SweepMaxAttempts caps automatic retries of a route_plan that keeps
	// failing (counted as durable route_failed events on the bundle).
	SweepMaxAttempts = 3
)

// SweepResult describes one bundle the startup sweep attempted to deliver.
type SweepResult struct {
	Bundle  string
	DestID  string
	Receipt Receipt
	Err     error
}

// SweepPendingRoutes makes route delivery at-least-once instead of tied to
// process lifetime: any bundle holding a durable route_plan but no routed
// event (the app quit, crashed, or failed before delivery) is re-routed.
// Bundles without session_end are skipped — they are still recording or died
// mid-capture, and half-written bundles are a CLI-recovery decision, not an
// automatic one. RouteByID appends the routed / route_failed provenance.
func SweepPendingRoutes(ctx context.Context, router *Router, meetingsDir string) []SweepResult {
	entries, err := os.ReadDir(meetingsDir)
	if err != nil {
		return nil
	}
	var (
		results  []SweepResult
		expanded bool
	)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return results
		}
		if !entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".meeting") {
			continue
		}
		bundle := filepath.Join(meetingsDir, entry.Name())
		destID, ok := pendingRoutePlan(bundle)
		if !ok {
			continue
		}
		if !expanded {
			// Resolve camp: destinations once, and only when something is pending.
			dctx, cancel := context.WithTimeout(ctx, DiscoverTimeout)
			if cfg, _, err := router.ExpandForRouting(dctx); err == nil {
				router.Cfg = cfg
			}
			cancel()
			expanded = true
		}
		results = append(results, sweepRoute(ctx, router, bundle, destID))
	}
	return results
}

// pendingRoutePlan reports the destination of an undelivered route plan.
func pendingRoutePlan(bundle string) (destID string, pending bool) {
	events, err := ReadEvents(bundleEventsPath(bundle))
	if err != nil {
		return "", false
	}
	var (
		plan     string
		ended    bool
		endedAt  time.Time
		routed   bool
		failures int
	)
	for _, e := range events {
		switch e.Type {
		case meetinglog.TypeRoutePlan:
			plan = e.Label
		case meetinglog.TypeSessionEnd:
			ended = true
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
				endedAt = t
			}
		case TypeRouted:
			routed = true
		case TypeRouteFailed:
			failures++
		}
	}
	if plan == "" || !ended || routed || failures >= SweepMaxAttempts {
		return "", false
	}
	if !endedAt.IsZero() && time.Since(endedAt) > SweepWindow {
		return "", false
	}
	return plan, true
}

func sweepRoute(ctx context.Context, router *Router, bundle, destID string) SweepResult {
	result := SweepResult{Bundle: bundle, DestID: destID}
	summary, err := LoadSummaryFromJSONL(bundleEventsPath(bundle))
	if err != nil {
		result.Err = err
		return result
	}
	note, err := Render(summary, router.Cfg.Body)
	if err != nil {
		result.Err = err
		return result
	}
	result.Receipt, result.Err = router.RouteByID(ctx, note, destID)
	return result
}
