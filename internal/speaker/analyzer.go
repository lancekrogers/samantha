package speaker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Analyzer coordinates windowing policy and a pluggable Engine.
// It is safe for concurrent Submit from one producer; Events is read-only.
type Analyzer struct {
	cfg    Config
	engine Engine

	mu     sync.Mutex
	closed bool
	events chan Event
}

// NewAnalyzer builds an analyzer. engine may be nil only when cfg is disabled;
// when enabled, engine is required.
func NewAnalyzer(cfg Config, engine Engine) (*Analyzer, error) {
	cfg = cfg.Normalize()
	if cfg.EffectiveEnabled() && engine == nil {
		return nil, fmt.Errorf("speaker: engine required when enabled")
	}
	return &Analyzer{
		cfg:    cfg,
		engine: engine,
		events: make(chan Event, 64),
	}, nil
}

// Events returns the event stream. Closed when Analyzer.Close is called.
func (a *Analyzer) Events() <-chan Event { return a.events }

// Finalize runs offline diarization when meeting analysis is active.
// When disabled or samples empty, returns an empty timeline and nil error.
func (a *Analyzer) Finalize(ctx context.Context, samples []float32) (Timeline, error) {
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Timeline{}, fmt.Errorf("speaker: analyzer closed")
	}
	if !a.cfg.MeetingActive() || a.engine == nil {
		return Timeline{FinalizedAt: time.Now()}, nil
	}
	if len(samples) == 0 {
		return Timeline{FinalizedAt: time.Now()}, nil
	}
	tl, err := a.engine.Diarize(ctx, samples, a.cfg.Meeting.NumSpeakers)
	if err != nil {
		return Timeline{}, err
	}
	tl.FinalizedAt = time.Now()
	// Best-effort notify listeners.
	select {
	case a.events <- Event{Kind: EventTimelineFinalized, Observation: Observation{State: StateStable, Source: SourceRecording}}:
	default:
	}
	return tl, nil
}

// IdentifySegment embeds samples and applies thresholded identification.
func (a *Analyzer) IdentifySegment(ctx context.Context, seg Segment) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	obs := Observation{
		SegmentID: seg.ID,
		Start:     seg.Start,
		End:       seg.End,
		Label:     LabelUnknown,
		State:     StateProvisional,
		Source:    seg.Source,
	}
	if !a.cfg.LiveActive() && !a.cfg.MeetingActive() {
		obs.State = StateRejected
		return obs, nil
	}
	if a.engine == nil || len(seg.Samples) == 0 {
		obs.Label = LabelUnknown
		obs.State = StateStable
		return obs, nil
	}
	emb, err := a.engine.Embed(ctx, seg.Samples)
	if err != nil {
		return obs, err
	}
	th := a.cfg.Normalize().Live.Threshold
	label, conf, err := a.engine.Identify(ctx, emb, th)
	if err != nil {
		return obs, err
	}
	obs.Label = ApplyThreshold(label, conf, th)
	obs.Confidence = conf
	obs.State = StateStable
	return obs, nil
}

// Close shuts down the event channel and engine.
func (a *Analyzer) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	close(a.events)
	if a.engine != nil {
		return a.engine.Close()
	}
	return nil
}
