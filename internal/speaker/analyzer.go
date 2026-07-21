package speaker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Analyzer is the product-facing seam for speaker analysis.
//
// Interim PR1 surface (design WI-afa621 full Submit/EndSegment lands with live
// windowing in a later PR):
//   - Events / Finalize / IdentifySegment / Close
//   - WindowMS is stored for the live worker; not consumed until Submit exists.
//
// Concurrency: all Engine method calls are serialized on engineMu so engines
// need only be single-worker safe. Finalize does not hold engineMu across
// nothing — it holds it for the Diarize call (required for Close safety) but
// does not hold the closed-state mutex during inference wait beyond the engine
// lock itself. Close waits for in-flight engine work by taking engineMu.
type Analyzer struct {
	cfg    Config
	engine Engine

	// stateMu protects closed and events lifecycle.
	stateMu  sync.Mutex
	closed   bool
	events   chan Event
	session  uint64
	sequence uint64

	// engineMu serializes all engine calls and Close of the engine.
	engineMu sync.Mutex
}

// NewAnalyzer builds an analyzer. engine may be nil only when no sub-path is
// active; when AnyActive, engine is required.
func NewAnalyzer(cfg Config, engine Engine) (*Analyzer, error) {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.AnyActive() && engine == nil {
		return nil, fmt.Errorf("speaker: engine required when live or meeting analysis is enabled")
	}
	return &Analyzer{
		cfg:     cfg,
		engine:  engine,
		events:  make(chan Event, 64),
		session: 1,
	}, nil
}

// Events returns the event stream. Closed when Analyzer.Close is called.
func (a *Analyzer) Events() <-chan Event { return a.events }

// Finalize runs offline diarization when meeting analysis is active.
// When disabled or samples empty, returns an empty timeline and nil error.
// Does not hold the closed-state lock across the entire call after snapshotting
// config; engine work is serialized via engineMu so Close cannot free the
// engine mid-Diarize.
func (a *Analyzer) Finalize(ctx context.Context, samples []float32) (Timeline, error) {
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return Timeline{}, fmt.Errorf("speaker: analyzer closed")
	}
	cfg := a.cfg
	eng := a.engine
	a.stateMu.Unlock()

	if !cfg.MeetingActive() || eng == nil {
		return Timeline{FinalizedAt: time.Now()}, nil
	}
	if len(samples) == 0 {
		return Timeline{FinalizedAt: time.Now()}, nil
	}

	a.engineMu.Lock()
	// Re-check closed under engineMu so we never call into a closed engine.
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		a.engineMu.Unlock()
		return Timeline{}, fmt.Errorf("speaker: analyzer closed")
	}
	a.stateMu.Unlock()

	tl, err := eng.Diarize(ctx, samples, cfg.Meeting.NumSpeakers)
	a.engineMu.Unlock()
	if err != nil {
		return Timeline{}, err
	}
	tl.FinalizedAt = time.Now()

	summary := Observation{
		Label:  LabelUnknown,
		State:  StateStable,
		Source: SourceRecording,
	}
	if n := len(tl.Observations); n > 0 {
		summary = tl.Observations[0]
		summary.SegmentID = fmt.Sprintf("timeline-%d", n)
	}
	tlCopy := tl.Clone()
	a.emit(Event{
		Kind:        EventTimelineFinalized,
		Observation: summary,
		Timeline:    &tlCopy,
	})
	return tl, nil
}

// IdentifySegment embeds samples and applies product thresholding.
// Empty samples or inactive config return StateRejected (not Stable unknown).
func (a *Analyzer) IdentifySegment(ctx context.Context, seg Segment) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	obs := Observation{
		SegmentID: seg.ID,
		StartMS:   MS(seg.Start),
		EndMS:     MS(seg.End),
		Label:     LabelUnknown,
		State:     StateProvisional,
		Source:    seg.Source,
	}

	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		obs.State = StateRejected
		return obs, fmt.Errorf("speaker: analyzer closed")
	}
	cfg := a.cfg
	eng := a.engine
	a.stateMu.Unlock()

	if !cfg.LiveActive() && !cfg.MeetingActive() {
		obs.State = StateRejected
		return obs, nil
	}
	if eng == nil || len(seg.Samples) == 0 {
		obs.State = StateRejected
		return obs, nil
	}

	th := identifyThreshold(cfg, seg.Source)

	a.engineMu.Lock()
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		a.engineMu.Unlock()
		obs.State = StateRejected
		return obs, fmt.Errorf("speaker: analyzer closed")
	}
	a.stateMu.Unlock()

	emb, err := eng.Embed(ctx, seg.Samples)
	if err != nil {
		a.engineMu.Unlock()
		return obs, err
	}
	label, conf, err := eng.Identify(ctx, emb)
	a.engineMu.Unlock()
	if err != nil {
		return obs, err
	}

	obs.Label = ApplyThreshold(label, conf, th)
	obs.Confidence = conf
	obs.State = StateStable
	return obs, nil
}

// identifyThreshold selects live vs meeting cutoff.
// Recording/fixture sources use meeting (root) threshold; live mic uses Live.
func identifyThreshold(cfg Config, src Source) float32 {
	cfg = cfg.Normalize()
	switch src {
	case SourceRecording, SourceFixture:
		return cfg.MeetingThreshold()
	default:
		if cfg.LiveActive() {
			return cfg.LiveThreshold()
		}
		return cfg.MeetingThreshold()
	}
}

func (a *Analyzer) emit(ev Event) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.closed {
		return
	}
	a.sequence++
	ev.Sequence = a.sequence
	ev.SessionID = fmt.Sprintf("session-%d", a.session)
	if ev.Timeline != nil {
		copy := ev.Timeline.Clone()
		ev.Timeline = &copy
	}
	select {
	case a.events <- ev:
	default:
		// Drop when full; never block capture/finalize on slow listeners.
	}
}

// Reset starts a new logical analysis session without rebuilding the native
// engine. It drains queued events and restarts deterministic sequence numbers.
func (a *Analyzer) Reset() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.closed {
		return fmt.Errorf("speaker: analyzer closed")
	}
	a.session++
	a.sequence = 0
	for {
		select {
		case <-a.events:
		default:
			return nil
		}
	}
}

// Close shuts down the event channel and engine. Waits for in-flight engine
// work (engineMu) before closing the native engine.
func (a *Analyzer) Close() error {
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return nil
	}
	a.closed = true
	close(a.events)
	a.stateMu.Unlock()

	a.engineMu.Lock()
	defer a.engineMu.Unlock()
	if a.engine != nil {
		return a.engine.Close()
	}
	return nil
}
