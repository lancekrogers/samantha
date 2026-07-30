package speaker

import (
	"context"
	"fmt"
	"sync"
)

// EngineBuilder constructs the live engine. It is called at most once per
// successful build and may be slow (native model load), so LazyLive always
// calls it off the caller's goroutine.
type EngineBuilder func() (Engine, error)

// LazyLive is a LiveSpeakerController that defers engine construction until
// live analysis is actually switched on.
//
// The startup path used to decide this once: config off meant an adapter with a
// nil analyzer, and SetEnabled could never recover it, so /speakers on reported
// "unavailable" forever. Building on demand lets chat turn labels on mid-session
// while installs that never enable speakers still pay nothing — no model load,
// no capture subscription.
//
// Engine loads run in the background because SetEnabled is called from the TUI
// update loop, which must not block. Status reports Running while a build is in
// flight and the 250ms stats poll picks up Healthy or Unavailable when it lands.
type LazyLive struct {
	ctx      context.Context
	cfg      Config
	capture  CaptureSource
	build    EngineBuilder
	capacity int

	mu       sync.Mutex
	adapter  *LiveAdapter
	analyzer *Analyzer
	stopFeed func()
	building bool
	closed   bool
	// desired is the last requested on/off state. A build takes long enough for
	// the user to change their mind, so the finished adapter adopts this rather
	// than the request that started the load.
	desired bool
	// status/lastErr describe the pre-build state; once adapter exists it owns
	// status reporting.
	status  LiveStatus
	lastErr string
}

// NewLazyLive returns a controller that builds its engine on first enable.
// A nil build or capture makes the controller permanently unavailable with
// detail as the reported error, which keeps callers free of nil checks.
func NewLazyLive(ctx context.Context, cfg Config, capture CaptureSource, build EngineBuilder, detail string) *LazyLive {
	if ctx == nil {
		ctx = context.Background()
	}
	// Reaching a build means live analysis was requested, so the analyzer must
	// see live as active regardless of what was on disk at launch. Without this
	// the on-demand path would load a model and then have IdentifySegment reject
	// every window, which is the same silent nothing-happens it exists to fix.
	analyzerCfg := cfg.Normalize()
	analyzerCfg.Enabled = true
	analyzerCfg.Live.Enabled = true

	l := &LazyLive{
		ctx:      ctx,
		cfg:      analyzerCfg,
		capture:  capture,
		build:    build,
		capacity: 4,
		status:   LiveDisabled,
	}
	if build == nil || capture == nil {
		l.status = LiveUnavailable
		l.lastErr = detail
	}
	return l
}

// SetEnabled turns live analysis on or off, building the engine on the first
// enable. It never blocks on the engine load.
func (l *LazyLive) SetEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.status == LiveUnavailable {
		return
	}
	l.desired = enabled
	if l.adapter != nil {
		l.adapter.SetEnabled(enabled)
		return
	}
	if !enabled {
		// A build already in flight keeps going; it will adopt desired=false
		// when it lands, so the engine stays warm for the next enable.
		l.status = LiveDisabled
		return
	}
	if l.building {
		return
	}
	l.building = true
	l.status = LiveRunning
	l.lastErr = ""
	go l.startEngine()
}

// startEngine performs the slow build and installs the adapter. On failure the
// controller reports Unavailable with the reason so /speakers status and the
// footer explain themselves instead of silently doing nothing.
func (l *LazyLive) startEngine() {
	engine, err := l.build()
	if err == nil && engine == nil {
		err = fmt.Errorf("speaker: engine builder returned no engine")
	}

	var (
		analyzer *Analyzer
		adapter  *LiveAdapter
		stopFeed func()
	)
	if err == nil {
		analyzer, err = NewAnalyzer(l.cfg, engine)
	}
	if err == nil {
		adapter = NewLiveAdapter(l.ctx, analyzer, l.capacity)
		stopFeed, err = StartLiveFeed(l.ctx, l.capture, adapter, l.cfg.Live.WindowMS)
	}

	l.mu.Lock()
	l.building = false
	// Close raced the build: tear the whole chain down rather than leaking a
	// feed and a native engine into a closed controller.
	if l.closed || err != nil {
		l.status = LiveUnavailable
		if err != nil {
			l.lastErr = err.Error()
		}
		if l.closed {
			l.status = LiveClosed
		}
		l.mu.Unlock()
		teardown(stopFeed, adapter, analyzer, engine)
		return
	}
	l.adapter, l.analyzer, l.stopFeed = adapter, analyzer, stopFeed
	desired := l.desired
	l.mu.Unlock()

	// The adapter comes up enabled; honor a disable that arrived mid-load.
	if !desired {
		adapter.SetEnabled(false)
	}
}

// Stats reports the adapter's view once built, and the pre-build state before
// that. Callers poll this, so it must never block on a build.
func (l *LazyLive) Stats() LiveStats {
	l.mu.Lock()
	adapter := l.adapter
	status, lastErr := l.status, l.lastErr
	l.mu.Unlock()
	if adapter != nil {
		return adapter.Stats()
	}
	return LiveStats{Status: status, Capacity: l.capacity, LastError: lastErr}
}

// Reset starts a new logical session when an engine is present.
func (l *LazyLive) Reset() error {
	l.mu.Lock()
	adapter := l.adapter
	l.mu.Unlock()
	if adapter == nil {
		return nil
	}
	return adapter.Reset()
}

// Close stops the feed and frees the engine. Safe to call when no build ever
// happened, and safe to call while one is in flight — startEngine tears down
// whatever it built once it sees closed.
func (l *LazyLive) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.status = LiveClosed
	adapter, analyzer, stopFeed := l.adapter, l.analyzer, l.stopFeed
	l.adapter, l.analyzer, l.stopFeed = nil, nil, nil
	l.mu.Unlock()

	teardown(stopFeed, adapter, analyzer, nil)
	return nil
}

// teardown releases a live chain in dependency order: drain the capture feed
// first so no frame lands on a closing adapter, then the adapter's worker, then
// the analyzer (which closes its engine). engine is only passed when the chain
// failed before an analyzer took ownership of it.
func teardown(stopFeed func(), adapter *LiveAdapter, analyzer *Analyzer, engine Engine) {
	if stopFeed != nil {
		stopFeed()
	}
	if adapter != nil {
		_ = adapter.Close()
	}
	if analyzer != nil {
		_ = analyzer.Close()
		return
	}
	if engine != nil {
		_ = engine.Close()
	}
}
