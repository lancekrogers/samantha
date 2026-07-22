//go:build !integration

package cmd

import (
	"context"
	"fmt"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/speaker"
)

// prepareLiveSpeaker builds a live speaker adapter when config enables it.
// On any setup failure it returns an unavailable adapter so conversation still
// starts; detail explains the degraded status for the event bus / UI.
//
// stop must be called before adapter.Close so the capture feed drains first;
// it also closes the analyzer (and its engine).
func prepareLiveSpeaker(
	ctx context.Context,
	cfg *config.Config,
	capture speaker.CaptureSource,
	_ func(string, float64),
) (adapter *speaker.LiveAdapter, stop func(), detail string) {
	noopStop := func() {}
	sp := speaker.FromAppConfig(cfg)
	if !sp.LiveActive() {
		return speaker.NewLiveAdapter(ctx, nil, 4), noopStop, ""
	}
	if textMode || capture == nil {
		return speaker.NewLiveAdapter(ctx, nil, 4), noopStop,
			"live speakers unavailable: microphone capture is required (not available in --text mode)"
	}

	// Prefer live-only engine (embedding) so chat does not require pyannote.
	// Fall back to the full meeting engine when live-only init fails.
	engine, err := speaker.NewSherpaLiveEngine(sp, config.ModelsDirFrom(cfg))
	if err != nil {
		engine, err = speaker.NewSherpaEngine(sp, config.ModelsDirFrom(cfg))
	}
	if err != nil {
		return speaker.NewLiveAdapter(ctx, nil, 4), noopStop,
			fmt.Sprintf("live speakers unavailable: %v", err)
	}

	analyzer, err := speaker.NewAnalyzer(sp, engine)
	if err != nil {
		_ = engine.Close()
		return speaker.NewLiveAdapter(ctx, nil, 4), noopStop,
			fmt.Sprintf("live speakers unavailable: %v", err)
	}

	adapter = speaker.NewLiveAdapter(ctx, analyzer, 4)
	stopFeed, feedErr := speaker.StartLiveFeed(ctx, capture, adapter, sp.Live.WindowMS)
	if feedErr != nil {
		_ = adapter.Close()
		_ = analyzer.Close()
		return speaker.NewLiveAdapter(ctx, nil, 4), noopStop,
			fmt.Sprintf("live speakers unavailable: %v", feedErr)
	}

	stop = func() {
		if stopFeed != nil {
			stopFeed()
		}
		// Adapter workers may still be finishing IdentifySegment; close
		// adapter first so process() exits, then free the analyzer/engine.
		_ = adapter.Close()
		_ = analyzer.Close()
	}
	return adapter, stop, "live speakers active (auto-label speaker-1..N)"
}
