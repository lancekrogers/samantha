package speaker

import (
	"context"
	"time"
)

// ReplayItem is a metadata-only event fixture. Replay operates on
// observations rather than audio so adapter tests never require model
// downloads or private recordings.
type ReplayItem struct {
	Event      Event
	Delay      time.Duration
	Drop       bool
	Duplicates int
}

// ReplayClock makes timing controllable in tests. The default clock honors
// item delays; tests can provide an immediate clock for deterministic runs.
type ReplayClock func(context.Context, time.Duration) error

// Replay replays a fixed event sequence, preserving fixture order even when
// observation timestamps are deliberately out of order. Drop and duplicate
// behavior model lossy or repeated async adapter delivery.
type Replay struct {
	Items []ReplayItem
	Clock ReplayClock
}

// Run emits owned event copies.
func (r Replay) Run(ctx context.Context, sink func(Event) error) error {
	if sink == nil {
		return context.Canceled
	}
	clock := r.Clock
	if clock == nil {
		clock = replaySleep
	}
	for _, item := range r.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := clock(ctx, item.Delay); err != nil {
			return err
		}
		if item.Drop {
			continue
		}
		count := item.Duplicates + 1
		if count < 1 {
			count = 1
		}
		for i := 0; i < count; i++ {
			if err := sink(cloneEvent(item.Event)); err != nil {
				return err
			}
		}
	}
	return nil
}

func replaySleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func cloneEvent(ev Event) Event {
	if ev.Timeline != nil {
		copy := ev.Timeline.Clone()
		ev.Timeline = &copy
	}
	return ev
}
