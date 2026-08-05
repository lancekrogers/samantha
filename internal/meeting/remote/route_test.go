package remote

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Routing consumes a *finished* meeting. These pin the gate: what has notes
// may route, what doesn't must say so, and a retried route answers from the
// cache instead of filing twice.

func TestSummaryOnlyAfterNotesExist(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)

	if _, err := session.Summary(); !errors.Is(err, ErrNotRoutable) {
		t.Fatalf("Summary() while recording error = %v, want ErrNotRoutable", err)
	}

	if err := session.AppendSegment(context.Background(), 0, pcm(7, 1600), time.Now()); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 0, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)

	if got := session.Status().State; got != StateReady {
		t.Fatalf("state = %q, want ready", got)
	}
	summary, err := session.Summary()
	if err != nil {
		t.Fatalf("Summary() after ready error = %v", err)
	}
	if summary.Bundle == "" {
		t.Fatal("Summary().Bundle is empty; routing needs the bundle path")
	}
}

func TestSummaryOnFailedMeetingIsNotRoutable(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(errors.New("stt fell over"))})
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(7, 1600), time.Now()); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 0, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitDone(t, session)

	if _, err := session.Summary(); !errors.Is(err, ErrNotRoutable) {
		t.Fatalf("Summary() on failed meeting error = %v, want ErrNotRoutable", err)
	}
}

// A janitor-abandoned meeting still produced real notes; it routes like a
// clean one. Interrupted is a fact about the ending, not the content.
func TestSummaryRoutableAfterJanitorAbandon(t *testing.T) {
	m, clock := janitorManager(t, newRecordingPipeline(nil))
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(7, 1600), clock.now); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}

	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now) // interrupted
	clock.advance(6 * time.Minute)
	m.Sweep(context.Background(), clock.now) // abandoned: processed + closed
	waitDone(t, session)

	if got := session.Status().State; got != StateInterrupted {
		t.Fatalf("state = %q, want interrupted", got)
	}
	if _, err := session.Summary(); err != nil {
		t.Fatalf("Summary() after abandon error = %v; interrupted notes must route", err)
	}
}

func TestRoutedReceiptCacheIsPerCampaign(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)

	if _, ok := session.RoutedFor("mytools"); ok {
		t.Fatal("RoutedFor() before any route reports a receipt")
	}
	receipt := RouteReceipt{Outcome: "routed", Destination: "mytools notes/meetings"}
	session.MarkRouted("mytools", receipt)

	got, ok := session.RoutedFor("mytools")
	if !ok || got != receipt {
		t.Fatalf("RoutedFor(mytools) = %+v, %v; want cached receipt", got, ok)
	}
	if _, ok := session.RoutedFor("othercampaign"); ok {
		t.Fatal("a different campaign must not answer from the cache")
	}
}
