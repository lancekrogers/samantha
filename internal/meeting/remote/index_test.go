package remote

import (
	"context"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
)

// TestManagerRehydratesBundlesAfterRestart is G33: a second manager over the
// same meetings dir — which is what a serve restart is — still knows every
// meeting on disk, and knows none of them as live.
func TestManagerRehydratesBundlesAfterRestart(t *testing.T) {
	root := t.TempDir()
	clock := &fakeClock{now: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}
	first := testManager(t, Options{Root: root, Pipeline: newRecordingPipeline(nil), Now: clock.Now})
	session, err := first.Start(context.Background(), StartRequest{
		Title:     "Weekly sync",
		RoutePlan: &RoutePlan{DestinationID: "docs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)

	restarted := testManager(t, Options{Root: root, Pipeline: newRecordingPipeline(nil)})
	entries, truncated, err := restarted.Index(context.Background(), meeting.IndexOptions{})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if truncated || len(entries) != 1 {
		t.Fatalf("entries = %d (truncated %v), want the one recorded bundle", len(entries), truncated)
	}
	if entries[0].Bundle != session.BundlePath() {
		t.Fatalf("bundle = %q, want %q", entries[0].Bundle, session.BundlePath())
	}
	if entries[0].State != meeting.BundleStateReady {
		t.Errorf("state = %q, want ready", entries[0].State)
	}
	if entries[0].Route == nil || entries[0].Route.DestinationID != "docs" {
		t.Errorf("route = %+v, want the plan it started with", entries[0].Route)
	}

	// Rehydration is read-only: the old live id is gone with the process, and
	// no rehydrated bundle may hold the single recording slot.
	if _, err := restarted.Session(session.ID()); err == nil {
		t.Error("a stale live id resolved after the restart")
	}
	if live := restarted.Live(); live != nil {
		t.Error("rehydration created a live session")
	}
	if restarted.Root() != root {
		t.Errorf("Root() = %q, want %q", restarted.Root(), root)
	}
}

func TestManagerIndexOfAnEmptyRootIsEmpty(t *testing.T) {
	m := testManager(t, Options{})
	entries, truncated, err := m.Index(context.Background(), meeting.IndexOptions{})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if len(entries) != 0 || truncated {
		t.Fatalf("entries = %d (truncated %v), want an empty history", len(entries), truncated)
	}
}
