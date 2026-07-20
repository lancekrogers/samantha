package meeting

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Runner executes an external command (camp, osascript). Tests inject fakes.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, err error)

// LookPath locates a binary on PATH.
type LookPath func(file string) (string, error)

// Router dispatches rendered notes to sinks.
type Router struct {
	Cfg      Config
	Run      Runner
	LookPath LookPath
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// GOOS overrides runtime.GOOS for tests (apple-notes gating).
	GOOS string
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Router) goos() string {
	if r.GOOS != "" {
		return r.GOOS
	}
	return runtime.GOOS
}

// RouteMeeting routes an already-rendered note to dest.
func (r *Router) RouteMeeting(ctx context.Context, note RenderedNote, dest Destination) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	sink, err := r.sinkFor(dest)
	if err != nil {
		return Receipt{
			DestinationID: dest.ID,
			Type:          dest.Type,
			Outcome:       OutcomeFailed,
			Detail:        err.Error(),
			At:            r.now(),
		}, err
	}
	receipt, err := sink.Route(ctx, note)
	if receipt.At.IsZero() {
		receipt.At = r.now()
	}
	if receipt.DestinationID == "" {
		receipt.DestinationID = dest.ID
	}
	if receipt.Type == "" {
		receipt.Type = dest.Type
	}
	if err != nil {
		if receipt.Outcome == "" {
			receipt.Outcome = OutcomeFailed
		}
		if receipt.Detail == "" {
			receipt.Detail = err.Error()
		}
		return receipt, err
	}
	if receipt.Outcome == "" {
		receipt.Outcome = OutcomeRouted
	}
	// Provenance: best-effort append; never fail the route on provenance write.
	_ = AppendRoutedEvent(note.SourceJSONL, receipt)
	return receipt, nil
}

// RouteByID looks up destID in config and routes.
func (r *Router) RouteByID(ctx context.Context, note RenderedNote, destID string) (Receipt, error) {
	cfg := r.Cfg.Normalize()
	dest, ok := cfg.DestinationByID(destID)
	if !ok {
		return Receipt{
			DestinationID: destID,
			Outcome:       OutcomeFailed,
			Detail:        fmt.Sprintf("unknown destination %q", destID),
			At:            r.now(),
		}, fmt.Errorf("meeting: unknown destination %q", destID)
	}
	return r.RouteMeeting(ctx, note, dest)
}

// AvailableDestinations filters destinations by platform and dependency presence.
func (r *Router) AvailableDestinations() []Destination {
	cfg := r.Cfg.Normalize()
	out := make([]Destination, 0, len(cfg.Destinations))
	for _, d := range cfg.Destinations {
		if r.destinationAvailable(d) {
			out = append(out, d)
		}
	}
	return out
}

func (r *Router) destinationAvailable(d Destination) bool {
	switch d.Type {
	case TypeCampaign:
		return r.campAvailable()
	case TypeFile:
		return strings.TrimSpace(d.Path) != ""
	case TypeAppleNotes:
		return r.goos() == "darwin"
	default:
		return false
	}
}

func (r *Router) campAvailable() bool {
	if r.LookPath == nil {
		return false
	}
	_, err := r.LookPath("camp")
	return err == nil
}

func (r *Router) sinkFor(dest Destination) (Sink, error) {
	switch dest.Type {
	case TypeFile:
		return FileSink{Dest: dest}, nil
	case TypeCampaign:
		if !r.campAvailable() {
			return nil, fmt.Errorf("meeting: camp not found on PATH")
		}
		return CampaignSink{Dest: dest, Run: r.Run, LookPath: r.LookPath}, nil
	case TypeAppleNotes:
		if r.goos() != "darwin" {
			return nil, fmt.Errorf("meeting: apple-notes only supported on macOS (delegated on iOS)")
		}
		return AppleNotesSink{Dest: dest, Run: r.Run, LookPath: r.LookPath}, nil
	default:
		return nil, fmt.Errorf("meeting: unknown destination type %q", dest.Type)
	}
}

// BannerLine formats a short human status for CLI/TUI banners.
func BannerLine(receipt Receipt) string {
	switch receipt.Outcome {
	case OutcomeSkipped:
		return "Meeting notes kept local only"
	case OutcomeDelegated:
		return fmt.Sprintf("Meeting notes delegated to host (%s)", receipt.Detail)
	case OutcomeFailed:
		return fmt.Sprintf("Meeting route failed (notes kept local): %s", receipt.Detail)
	case OutcomeRouted:
		if receipt.Detail != "" {
			return fmt.Sprintf("Meeting notes routed to %s: %s", receipt.DestinationID, receipt.Detail)
		}
		return fmt.Sprintf("Meeting notes routed to %s", receipt.DestinationID)
	default:
		return fmt.Sprintf("Meeting route: %s", receipt.Outcome)
	}
}
