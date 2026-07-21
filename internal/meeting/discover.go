package meeting

import (
	"context"
	"time"
)

// DiscoverTimeout bounds camp list and similar probes used for pickers.
const DiscoverTimeout = 3 * time.Second

// DiscoverDestinations returns configured destinations that are available on
// this machine, plus campaigns from `camp list --json` when camp is installed.
//
// Camp list failures are soft for the destination set (configured base still
// returns) but the error is non-nil so UIs can surface "camp list failed".
// When camp is not on PATH, err is nil (nothing to discover).
func (r *Router) DiscoverDestinations(ctx context.Context) ([]Destination, error) {
	base := r.AvailableDestinations()
	if r.Run == nil || !r.campAvailable() {
		return base, nil
	}
	dctx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		dctx, cancel = context.WithTimeout(ctx, DiscoverTimeout)
		defer cancel()
	}
	camps, err := ListCampaigns(dctx, r.Run, r.LookPath)
	if err != nil {
		return base, err
	}
	if len(camps) == 0 {
		return base, nil
	}
	discovered := DestinationsFromCampaigns(camps)
	// Only keep discovered entries that pass availability (camp on PATH, etc.).
	available := make([]Destination, 0, len(discovered))
	for _, d := range discovered {
		if r.destinationAvailable(d) {
			available = append(available, d)
		}
	}
	return MergeDestinations(base, available), nil
}

// WithDestination ensures dest is present in cfg.Destinations (by id) so
// RouteByID and picker state can reference ephemeral camp discoveries.
func WithDestination(cfg Config, dest Destination) Config {
	cfg = cfg.Normalize()
	if dest.ID == "" {
		return cfg
	}
	if _, ok := cfg.DestinationByID(dest.ID); ok {
		return cfg
	}
	cfg.Destinations = append(cfg.Destinations, dest)
	return cfg
}

// ResolveDestination finds destID in cfg, then among discovered (if provided).
func ResolveDestination(cfg Config, destID string, discovered []Destination) (Destination, bool) {
	if destID == "" {
		return Destination{}, false
	}
	if d, ok := cfg.DestinationByID(destID); ok {
		return d, true
	}
	for _, d := range discovered {
		if d.ID == destID {
			return d, true
		}
	}
	return Destination{}, false
}
