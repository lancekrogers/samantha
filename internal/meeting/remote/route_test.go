package remote

import (
	"context"
	"errors"
	"sync/atomic"
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

// The idempotency core: N concurrent calls for one key share exactly one
// execution, later calls answer from the cache, a different key executes its
// own route, and a failure is forgotten so the retry is real.
func TestRouteOnceSingleFlightsAndCaches(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)

	var calls int32
	gate := make(chan struct{})
	fn := func() (RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		<-gate // hold every concurrent caller in the same in-flight window
		return RouteReceipt{Outcome: "routed", Destination: "mytools notes/meetings"}, nil
	}

	const racers = 8
	receipts := make(chan RouteReceipt, racers)
	for range racers {
		go func() {
			receipt, err := session.RouteOnce("meeting\x00mytools", fn)
			if err != nil {
				t.Errorf("RouteOnce() error = %v", err)
			}
			receipts <- receipt
		}()
	}
	close(gate)
	for range racers {
		if got := <-receipts; got.Destination != "mytools notes/meetings" {
			t.Fatalf("receipt = %+v", got)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("route executed %d times for %d concurrent calls, want 1", got, racers)
	}

	// Same key later: cache. Different key: new execution.
	if _, err := session.RouteOnce("meeting\x00mytools", fn); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cached key re-executed (calls = %d)", got)
	}
	_, _ = session.RouteOnce("intent\x00mytools", func() (RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		return RouteReceipt{Outcome: "routed"}, nil
	})
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("distinct capture key did not execute (calls = %d)", got)
	}
}

func TestRouteOnceForgetsFailures(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)

	failed := errors.New("camp offline")
	if _, err := session.RouteOnce("meeting\x00mytools", func() (RouteReceipt, error) {
		return RouteReceipt{}, failed
	}); !errors.Is(err, failed) {
		t.Fatalf("RouteOnce() error = %v, want the sink failure", err)
	}
	receipt, err := session.RouteOnce("meeting\x00mytools", func() (RouteReceipt, error) {
		return RouteReceipt{Outcome: "routed"}, nil
	})
	if err != nil || receipt.Outcome != "routed" {
		t.Fatalf("retry after failure = %+v, %v; want a real re-execution", receipt, err)
	}
}

// The step is visible only while processing — never stale on a terminal state.
func TestStatusReportsPipelineStep(t *testing.T) {
	pipe := newRecordingPipeline(nil)
	stepSeen := make(chan string, 4)
	pipe.before = func(job Job) {
		job.Step("transcribing")
		stepSeen <- "reported"
	}
	m := testManager(t, Options{Pipeline: pipe})
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(7, 1600), time.Now()); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	if _, err := session.Stop(context.Background(), 0, time.Now()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	<-stepSeen
	if got := session.Status().Step; got != "transcribing" {
		t.Fatalf("Step during processing = %q, want transcribing", got)
	}
	waitDone(t, session)
	if got := session.Status().Step; got != "" {
		t.Fatalf("Step after ready = %q, want empty", got)
	}
}
