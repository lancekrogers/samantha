//go:build !integration

package cmd

import (
	"context"
	"fmt"

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
	engineCfg := sp.Normalize()
	engineCfg.Enabled = true
	engineCfg.Live.Enabled = true
	build := liveSpeakerEngineBuilder(ctx, cfg, config.EnsureRuntimeAssets,
		func() (speaker.Engine, error) {
			// Prefer live-only engine (embedding) so chat does not require pyannote.
			return speaker.NewSherpaLiveEngine(engineCfg, modelsDir)
		}, func() (speaker.Engine, error) {
			// Fall back to the full meeting engine when live-only init fails.
			return speaker.NewSherpaEngine(engineCfg, modelsDir)
		})

	controller = speaker.NewLazyLive(ctx, sp, capture, build, "")
	stop = func() { _ = controller.Close() }

	if !sp.LiveActive() {
		// Off by config, but reachable: no model is loaded until /speakers on.
		return controller, stop, ""
	}
	controller.SetEnabled(true)
	return controller, stop, "live speakers starting (auto-label speaker-1..N)"
}

// liveSpeakerEngineBuilder ensures the managed embedding before the lazy first
// build. EnsureRuntimeAssets verifies existing files and skips their download,
// so repeated application starts remain network-free once TitaNet is present.
// A copied, enabled config is used only for manifest selection; /speakers on is
// session-local and must not silently persist the setting.
func liveSpeakerEngineBuilder(
	ctx context.Context,
	cfg *config.Config,
	ensure ensureAssetsFunc,
	buildLive speaker.EngineBuilder,
	buildFull speaker.EngineBuilder,
) speaker.EngineBuilder {
	assetCfg := config.Config{}
	if cfg != nil {
		assetCfg = *cfg
	}
	assetCfg.Speaker.Enabled = true
	assetCfg.Speaker.Live.Enabled = true

	return func() (speaker.Engine, error) {
		if err := ensure(ctx, &assetCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
			return nil, fmt.Errorf("ensure live speaker model: %w", err)
		}
		engine, err := buildLive()
		if err != nil && buildFull != nil {
			engine, err = buildFull()
		}
		if err == nil {
			seedEnrolledProfiles(cfg, engine, nil)
		}
		return engine, err
	}
}
