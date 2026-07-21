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
	err     error
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
	if f.err != nil {
		return Observation{}, f.err
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
	deadline := time.After(time.Second)
	for {
		if a.Stats().Processed == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("processed = %d, want two accepted frames", a.Stats().Processed)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stats := a.Stats()
	if stats.AnalyzerNanos == 0 || stats.LastAnalyzerNanos == 0 || stats.ResponsePathNanos == 0 || stats.LastResponsePathNanos == 0 {
		t.Fatalf("timing stats = %+v, want analyzer and response-path timings", stats)
	}
}

func TestLiveAdapterResponsePathDoesNotWaitForAnalyzer(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "slow", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	<-fake.started

	started := time.Now()
	if err := a.Submit(context.Background(), Segment{ID: "queued", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("response-path submit took %s while analyzer was blocked", elapsed)
	}
	if got := a.Stats().Processed; got != 0 {
		t.Fatalf("processed = %d before analyzer release, want 0", got)
	}
	close(fake.release)
}

func TestLiveAdapterDisabledBaselineIsIndependent(t *testing.T) {
	a := NewLiveAdapter(context.Background(), nil, 1)
	defer a.Close()
	started := time.Now()
	err := a.Submit(context.Background(), Segment{ID: "baseline", Samples: []float32{1}})
	if !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("disabled submit = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("disabled baseline submit took %s", elapsed)
	}
	if stats := a.Stats(); stats.Processed != 0 || stats.AnalyzerNanos != 0 {
		t.Fatalf("disabled baseline stats = %+v", stats)
	}
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

func TestLiveAdapterDropsInFlightResultsAcrossReset(t *testing.T) {
	fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "old", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	close(fake.release)
	select {
	case ev := <-a.Events():
		t.Fatalf("stale event after reset = %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if err := a.Submit(context.Background(), Segment{ID: "new", Samples: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	ev := awaitLiveEvent(t, a.Events())
	if ev.Observation.SegmentID != "new" || ev.SessionID != "session-2" || ev.Sequence != 1 {
		t.Fatalf("new session event = %+v", ev)
	}
}

func TestLiveAdapterPreservesErrorContextAndUnavailableState(t *testing.T) {
	fake := &liveFake{err: errors.New("engine unavailable")}
	a := NewLiveAdapter(context.Background(), fake, 1)
	defer a.Close()
	if err := a.Submit(context.Background(), Segment{ID: "failed", Start: time.Second, End: 2 * time.Second, Source: SourceLocalMic}); err != nil {
		t.Fatal(err)
	}
	ev := awaitLiveEvent(t, a.Events())
	if ev.Kind != EventSpeakerUpdated || ev.Observation.SegmentID != "failed" || ev.Observation.StartMS != 1000 || ev.Observation.EndMS != 2000 || ev.Observation.Source != SourceLocalMic || ev.Observation.State != StateRejected {
		t.Fatalf("error event = %+v", ev)
	}

	unavailable := NewLiveAdapter(context.Background(), nil, 1)
	defer unavailable.Close()
	unavailable.SetEnabled(true)
	if got := unavailable.Stats().Status; got != LiveUnavailable {
		t.Fatalf("unavailable status after enable = %q", got)
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

func BenchmarkLiveAdapterSubmit(b *testing.B) {
	b.Run("disabled_baseline", func(b *testing.B) {
		a := NewLiveAdapter(context.Background(), nil, 1)
		defer a.Close()
		segment := Segment{ID: "baseline", Samples: []float32{1}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := a.Submit(context.Background(), segment); !errors.Is(err, ErrLiveDisabled) {
				b.Fatalf("submit = %v", err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(a.Stats().LastResponsePathNanos), "last-submit-ns")
	})

	b.Run("enabled_overloaded", func(b *testing.B) {
		fake := &liveFake{started: make(chan struct{}, 1), release: make(chan struct{})}
		a := NewLiveAdapter(context.Background(), fake, 1)
		defer func() {
			close(fake.release)
			a.Close()
		}()
		if err := a.Submit(context.Background(), Segment{ID: "blocking", Samples: []float32{1}}); err != nil {
			b.Fatal(err)
		}
		<-fake.started
		segment := Segment{ID: "overloaded", Samples: []float32{1}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = a.Submit(context.Background(), segment)
		}
		b.StopTimer()
		b.ReportMetric(float64(a.Stats().LastResponsePathNanos), "last-submit-ns")
	})
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
