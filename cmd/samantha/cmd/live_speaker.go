//go:build !integration

package cmd

import (
	"context"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/speaker"
)

// prepareLiveSpeaker builds the live speaker controller for a conversation.
//
// The engine is loaded lazily: config that enables live analysis gets it
// switched on here, and config that does not still gets a working controller so
// /speakers on can turn labels on mid-session. Only a runtime that cannot feed
// audio at all (--text, no capture) is permanently unavailable; detail explains
// that on the event bus.
//
// stop tears the chain down in order (feed, adapter, analyzer/engine) and is
// safe to call when no engine was ever built.
func prepareLiveSpeaker(
	ctx context.Context,
	cfg *config.Config,
	capture speaker.CaptureSource,
	_ func(string, float64),
) (controller *speaker.LazyLive, stop func(), detail string) {
	sp := speaker.FromAppConfig(cfg)

	if textMode || capture == nil {
		unavailable := speaker.NewLazyLive(ctx, sp, nil, nil,
			"microphone capture is required (not available in --text mode)")
		return unavailable, func() { _ = unavailable.Close() },
			"live speakers unavailable: microphone capture is required (not available in --text mode)"
	}

	modelsDir := config.ModelsDirFrom(cfg)
	build := func() (speaker.Engine, error) {
		// Prefer live-only engine (embedding) so chat does not require pyannote.
		// Fall back to the full meeting engine when live-only init fails.
		engine, err := speaker.NewSherpaLiveEngine(sp, modelsDir)
		if err != nil {
			engine, err = speaker.NewSherpaEngine(sp, modelsDir)
		}
		if err != nil {
			return nil, err
		}
		return engine, nil
	}

	controller = speaker.NewLazyLive(ctx, sp, capture, build, "")
	stop = func() { _ = controller.Close() }

	if !sp.LiveActive() {
		// Off by config, but reachable: no model is loaded until /speakers on.
		return controller, stop, ""
	}
	controller.SetEnabled(true)
	return controller, stop, "live speakers starting (auto-label speaker-1..N)"
}
