package speaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type liveFake struct {
	started chan struct{}
	release chan struct{}
	label   string
}

func (f *liveFake) IdentifySegment(ctx context.Context, seg Segment) (Observation, error) {
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		case <-f.release:
		}
	}
	label := f.label
	if label == "" && len(seg.Samples) > 0 && seg.Samples[0] > 1 {
		label = "bob"
	}
	if label == "" {
		label = "alice"
	}
	return Observation{SegmentID: seg.ID, StartMS: MS(seg.Start), EndMS: MS(seg.End), Label: label, Confidence: .9, State: StateStable, Source: seg.Source}, nil
}

func (f *liveFake) Reset() error { return nil }

func TestLiveAdapterDropsWithoutBlockingAndCopiesFrames(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	samples := []float32{1}
	if err := a.Submit(context.Background(), Segment{ID: "one", Samples: samples}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	samples[0] = 99
	if err := a.Submit(context.Background(), Segment{ID: "two", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{ID: "three", Samples: []float32{1}}); !errors.Is(err, ErrLiveDropped) {
		t.Fatalf("saturated submit error = %v, want ErrLiveDropped", err)
	}
	if stats := a.Stats(); stats.Dropped != 1 || stats.QueueDepth != 1 {
		t.Fatalf("stats = %+v, want one dropped and one queued", stats)
	}
	close(fake.release)
}

func TestLiveAdapterEventsAreAsyncAndSessionOrdered(t *testing.T) {
	a := NewLiveAdapter(context.Background(), &liveFake{}, 2)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "one", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	first := awaitLiveEvent(t, a.Events())
	if first.Kind != EventSpeakerUpdated || first.SessionID != "session-1" || first.Sequence != 1 {
		t.Fatalf("first = %+v", first)
	}
	if err := a.Submit(context.Background(), Segment{ID: "two", Samples: []float32{2}}); err != nil {
		t.Fatal(err)
	}
	second := awaitLiveEvent(t, a.Events())
	if second.Kind != EventSpeakerChanged || second.Sequence != 2 {
		t.Fatalf("second = %+v", second)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{ID: "three", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	third := awaitLiveEvent(t, a.Events())
	if third.SessionID != "session-2" || third.Sequence != 1 {
		t.Fatalf("reset event = %+v", third)
	}
	a.SetEnabled(false)
	if err := a.Submit(context.Background(), Segment{ID: "four"}); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("disabled submit = %v", err)
	}
}

func TestLiveAdapterUnavailableAndClose(t *testing.T) {
	a := NewLiveAdapter(context.Background(), nil, 1)
	if got := a.Stats().Status; got != LiveUnavailable {
		t.Fatalf("status = %q", got)
	}
	if err := a.Submit(context.Background(), Segment{}); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("unavailable submit = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit(context.Background(), Segment{}); !errors.Is(err, ErrLiveClosed) {
		t.Fatalf("closed submit = %v", err)
	}
	select {
	case _, ok := <-a.Events():
		if ok {
			t.Fatal("events should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("events did not close")
	}
}

func awaitLiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
		return Event{}
	}
}
