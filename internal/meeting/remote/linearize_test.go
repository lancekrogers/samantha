package remote

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The acked-audio guarantee under concurrency: a segment accepted before Stop
// begins finalizing must be in the assembled meeting, no matter how the two
// interleave. putGate parks the upload between acceptance and its disk write,
// which is exactly the window the race lived in.
func TestStopWaitsForInFlightSegment(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)

	entered := make(chan struct{})
	release := make(chan struct{})
	session.putGate = func() {
		close(entered)
		<-release
	}

	uploadDone := make(chan error, 1)
	go func() {
		uploadDone <- session.AppendSegment(context.Background(), 0, pcm(7, 1600), time.Now())
	}()
	<-entered
	session.putGate = nil

	stopDone := make(chan Status, 1)
	go func() {
		status, err := session.Stop(context.Background(), -1, time.Now())
		if err != nil {
			t.Errorf("Stop() error = %v", err)
		}
		stopDone <- status
	}()

	// Stop must be parked on the in-flight upload, not finalizing without it.
	select {
	case <-stopDone:
		t.Fatal("Stop() finalized while an accepted segment was still mid-write")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-uploadDone; err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}
	status := <-stopDone
	if status.State != StateProcessing {
		t.Fatalf("state = %q, want processing", status.State)
	}
	if len(status.MissingSeqs) != 0 {
		t.Fatalf("missing = %v; the in-flight segment was not reconciled", status.MissingSeqs)
	}
	waitDone(t, session)
	if got := session.Status().Result; got == nil {
		t.Fatal("no result after processing")
	}
}

// New uploads arriving during the finalize window are refused with the same
// error a processing meeting reports — the client's gap re-push path recovers
// if the stop then reports missing audio.
func TestSegmentDuringFinalizeIsRefused(t *testing.T) {
	m := testManager(t, Options{Pipeline: newRecordingPipeline(nil)})
	session := startSession(t, m)
	if err := session.AppendSegment(context.Background(), 0, pcm(7, 1600), time.Now()); err != nil {
		t.Fatalf("AppendSegment() error = %v", err)
	}

	// Freeze Stop inside the barrier by parking an in-flight upload.
	entered := make(chan struct{})
	release := make(chan struct{})
	session.putGate = func() {
		close(entered)
		<-release
	}
	segDone := make(chan struct{})
	go func() {
		defer close(segDone)
		_ = session.AppendSegment(context.Background(), 1, pcm(8, 1600), time.Now())
	}()
	<-entered
	session.putGate = nil
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		_, _ = session.Stop(context.Background(), -1, time.Now())
	}()

	// Wait until Stop has raised the barrier, then try a new upload.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := session.AppendSegment(context.Background(), 2, pcm(9, 1600), time.Now())
		if errors.Is(err, ErrProcessing) {
			break
		}
		if err != nil {
			t.Fatalf("AppendSegment() unexpected error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("finalize barrier never refused a new upload")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	// Join both writers before the test returns: TempDir cleanup races the
	// parked upload and Stop's finalize otherwise ("segments: directory not
	// empty" under load).
	for name, ch := range map[string]chan struct{}{"upload": segDone, "stop": stopDone} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s goroutine did not finish", name)
		}
	}
	waitDone(t, session)
}
