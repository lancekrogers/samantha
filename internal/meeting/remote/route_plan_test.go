package remote

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// planManager is a manager whose plan delivery is recorded rather than run.
func planManager(t *testing.T, deliver RoutePlanFunc) (*Manager, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}
	m := testManager(t, Options{
		Pipeline:  newRecordingPipeline(nil),
		Now:       clock.Now,
		RoutePlan: deliver,
	})
	return m, clock
}

func TestStartRejectsMalformedRoutePlan(t *testing.T) {
	tests := []struct {
		name string
		plan *RoutePlan
		want error
	}{
		{"no destination", &RoutePlan{}, ErrRoutePlanDestination},
		{"blank destination", &RoutePlan{DestinationID: "   "}, ErrRoutePlanDestination},
		{"unknown body", &RoutePlan{DestinationID: "camp:blockhead", Body: "summary"}, ErrRoutePlanBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := planManager(t, nil)
			session, err := m.Start(context.Background(), StartRequest{Title: "Sync", RoutePlan: tt.plan})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Start() error = %v, want %v", err, tt.want)
			}
			if session != nil {
				t.Fatal("a rejected start returned a session")
			}
			// A refused plan must not leave a bundle behind, or the meetings
			// list fills with empty directories nobody asked for.
			if live := m.Live(); live != nil {
				t.Fatal("a rejected start left a live meeting")
			}
		})
	}
}

func TestRoutePlanIsWrittenBeforeAnySegment(t *testing.T) {
	m, clock := planManager(t, nil)
	session, err := m.Start(context.Background(), StartRequest{
		Title:     "Weekly sync",
		RoutePlan: &RoutePlan{DestinationID: "camp:blockhead", Body: meeting.BodyFull},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := session.AppendSegment(context.Background(), 0, pcm(1, 80), clock.now); err != nil {
		t.Fatal(err)
	}
	if err := session.Control(context.Background(),
		ControlRequest{Action: "bookmark", OffsetMs: 100}, clock.now); err != nil {
		t.Fatal(err)
	}

	events := decodeEvents(t, session.BundlePath())
	if len(events) < 2 {
		t.Fatalf("events = %+v, want at least session_start and route_plan", events)
	}
	if events[0].Type != meetinglog.TypeSessionStart {
		t.Fatalf("first event = %q, want session_start", events[0].Type)
	}
	if events[1].Type != meetinglog.TypeRoutePlan {
		t.Fatalf("second event = %q, want route_plan before anything else", events[1].Type)
	}
	if events[1].Label != "camp:blockhead" || events[1].Text != meeting.BodyFull {
		t.Fatalf("route_plan = %+v, want camp:blockhead / full", events[1])
	}
}

func TestRoutePlanDeliveredOnceAtPublish(t *testing.T) {
	var (
		calls   int32
		gotDest string
		gotBody string
	)
	deliver := func(ctx context.Context, summary meetinglog.Summary, destID, body string) (RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		gotDest, gotBody = destID, body
		if summary.Bundle == "" {
			t.Error("plan delivery got a summary with no bundle")
		}
		return RouteReceipt{Outcome: meeting.OutcomeRouted, Detail: "notes/meetings/x.md"}, nil
	}
	m, clock := planManager(t, deliver)
	session, err := m.Start(context.Background(), StartRequest{
		Title:     "Weekly sync",
		RoutePlan: &RoutePlan{DestinationID: "camp:blockhead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("plan delivered %d times, want exactly 1", got)
	}
	if gotDest != "camp:blockhead" || gotBody != "" {
		t.Fatalf("delivery args = %q/%q, want camp:blockhead and the configured body", gotDest, gotBody)
	}
	// A manual route of the same campaign shares the plan's execution rather
	// than filing the meeting a second time.
	if _, err := session.RouteOnce(CampaignRouteKey("", "blockhead"), func() (RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		return RouteReceipt{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a manual campaign route re-filed the meeting (calls = %d)", got)
	}
}

// TestRoutePlanFailureStaysSweepRetryable is the failure contract: delivery
// errors are absorbed, the meeting still reads ready, and the bundle keeps a
// plan with no routed event — exactly what SweepPendingRoutes looks for.
func TestRoutePlanFailureStaysSweepRetryable(t *testing.T) {
	deliver := func(ctx context.Context, summary meetinglog.Summary, destID, body string) (RouteReceipt, error) {
		// Mirror the router: a failed route leaves durable provenance.
		if err := meeting.AppendRouteFailedEvent(summary.JSONLFile, meeting.Receipt{
			DestinationID: destID, Outcome: meeting.OutcomeFailed, Detail: "camp: no such campaign",
		}); err != nil {
			t.Errorf("append route_failed: %v", err)
		}
		return RouteReceipt{}, errors.New("camp: no such campaign")
	}
	m, clock := planManager(t, deliver)
	session, err := m.Start(context.Background(), StartRequest{
		Title:     "Weekly sync",
		RoutePlan: &RoutePlan{DestinationID: "camp:missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)

	status := session.Status()
	if status.State != StateReady {
		t.Fatalf("state = %q, want ready — a route failure is not a recording failure", status.State)
	}
	if status.Error != "" {
		t.Errorf("status error = %q, want the route failure kept to the bundle's provenance", status.Error)
	}
	var planned, failed, routed int
	for _, event := range decodeEvents(t, session.BundlePath()) {
		switch event.Type {
		case meetinglog.TypeRoutePlan:
			planned++
		case meeting.TypeRouteFailed:
			failed++
		case meeting.TypeRouted:
			routed++
		}
	}
	if planned != 1 || failed != 1 || routed != 0 {
		t.Fatalf("provenance = %d plan / %d failed / %d routed, want 1/1/0", planned, failed, routed)
	}
}

func TestNilRoutePlanFuncRecordsThePlanWithoutError(t *testing.T) {
	m, clock := planManager(t, nil)
	session, err := m.Start(context.Background(), StartRequest{
		Title:     "Weekly sync",
		RoutePlan: &RoutePlan{DestinationID: "docs", Body: meeting.BodyNotes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	if got := session.Status().State; got != StateReady {
		t.Fatalf("state = %q, want ready", got)
	}
	var plans int
	for _, event := range decodeEvents(t, session.BundlePath()) {
		if event.Type == meetinglog.TypeRoutePlan {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("route_plan events = %d, want 1 for the sweep to find", plans)
	}
}

func TestStartWithoutRoutePlanWritesNone(t *testing.T) {
	m, clock := planManager(t, func(context.Context, meetinglog.Summary, string, string) (RouteReceipt, error) {
		t.Error("delivery ran for a meeting with no plan")
		return RouteReceipt{}, nil
	})
	session := startSession(t, m)
	if _, err := session.Stop(context.Background(), -1, clock.now); err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	for _, event := range decodeEvents(t, session.BundlePath()) {
		if event.Type == meetinglog.TypeRoutePlan {
			t.Fatal("a meeting with no route_plan wrote one anyway")
		}
	}
}

func TestRoutePlanKeySpace(t *testing.T) {
	tests := []struct {
		name   string
		destID string
		want   string
	}{
		{"campaign shares the wire route key", "camp:blockhead", CampaignRouteKey("", "blockhead")},
		{"configured destination has its own key", "docs", "destination\x00docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (routePlan{destID: tt.destID}).key(); got != tt.want {
				t.Fatalf("key() = %q, want %q", got, tt.want)
			}
		})
	}
}
