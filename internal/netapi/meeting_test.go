package netapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// meetingHarness is a live serve with the meeting surface enabled and a
// pipeline that does nothing, so tests exercise the wire rather than STT.
type meetingHarness struct {
	addr    string
	token   string
	client  *http.Client
	manager *remote.Manager
}

func newMeetingHarness(t *testing.T, opts remote.Options) *meetingHarness {
	return newRoutedMeetingHarness(t, opts, nil)
}

func newRoutedMeetingHarness(t *testing.T, opts remote.Options, route RouteMeetingFunc) *meetingHarness {
	t.Helper()
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	if opts.Pipeline == nil {
		opts.Pipeline = remote.PipelineFunc(func(context.Context, remote.Job) error { return nil })
	}
	manager, err := remote.NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	disp := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go disp.Run(context.Background())
	srv := New(Options{
		Bind:         "127.0.0.1:0",
		Credentials:  creds,
		Bus:          bus,
		Dispatcher:   disp,
		Meetings:     manager,
		RouteMeeting: route,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	return &meetingHarness{
		addr: waitAddr(t, srv), token: creds.Token,
		client: insecureHTTPClient(), manager: manager,
	}
}

func (h *meetingHarness) do(t *testing.T, method, path string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+h.addr+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *meetingHarness) startMeeting(t *testing.T) remote.StartResponse {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/v1/meeting/start", strings.NewReader(`{"title":"Standup"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("start status = %d, body = %s", resp.StatusCode, raw)
	}
	var out remote.StartResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func segmentBody(samples int) *bytes.Reader {
	raw := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(i))
	}
	return bytes.NewReader(raw)
}

func TestMeetingStartAdvertisesCaptureSettings(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{SegmentSeconds: 5, OutboxCapSegments: 120})
	started := h.startMeeting(t)
	if started.MeetingID == "" {
		t.Fatal("start returned no meeting_id")
	}
	if started.SegmentSeconds != 5 || started.OutboxCapSegments != 120 {
		t.Fatalf("capture settings = %+v, want 5s / 120 segments", started)
	}
}

func TestMeetingStartConflictsWhileRecording(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	h.startMeeting(t)
	resp := h.do(t, http.MethodPost, "/v1/meeting/start", strings.NewReader(`{}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second start = %d, want 409", resp.StatusCode)
	}
}

func TestMeetingRequiresAuth(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	req, err := http.NewRequest(http.MethodPost, "https://"+h.addr+"/v1/meeting/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated start = %d, want 401", resp.StatusCode)
	}
}

func TestMeetingUnknownIDIs404(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodGet, "/v1/meeting/deadbeef", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMeetingLifecycleOverTheWire(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	base := "/v1/meeting/" + started.MeetingID

	resp := h.do(t, http.MethodPut, base+"/segments/0", segmentBody(80), "application/octet-stream")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("segment upload = %d, want 204", resp.StatusCode)
	}
	// Re-uploading the same sequence is the client's retry path: same ack.
	resp = h.do(t, http.MethodPut, base+"/segments/0", segmentBody(80), "application/octet-stream")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("duplicate segment = %d, want 204", resp.StatusCode)
	}

	resp = h.do(t, http.MethodPost, base+"/control",
		strings.NewReader(`{"action":"bookmark","offset_ms":1500,"text":"decision"}`), "application/json")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control = %d, want 200", resp.StatusCode)
	}

	// Stopping past the delivered audio reports the gap instead of finalizing.
	var status remote.Status
	resp = h.do(t, http.MethodPost, base+"/stop", strings.NewReader(`{"last_seq":2}`), "application/json")
	decodeJSON(t, resp, &status)
	if len(status.MissingSeqs) != 2 || status.State != remote.StateRecording {
		t.Fatalf("status = %+v, want recording with two gaps", status)
	}

	resp = h.do(t, http.MethodPost, base+"/stop", strings.NewReader(`{"last_seq":0}`), "application/json")
	decodeJSON(t, resp, &status)
	if status.State != remote.StateProcessing {
		t.Fatalf("state = %q, want processing", status.State)
	}

	waitForState(t, h, started.MeetingID, remote.StateReady)
}

func TestMeetingSegmentRejectsMalformedBodies(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	base := "/v1/meeting/" + started.MeetingID

	tests := []struct {
		name string
		path string
		body io.Reader
		want int
	}{
		{"odd byte count is not whole samples", base + "/segments/0", strings.NewReader("abc"), http.StatusBadRequest},
		{"empty body", base + "/segments/0", strings.NewReader(""), http.StatusBadRequest},
		{"non-numeric sequence", base + "/segments/abc", segmentBody(8), http.StatusBadRequest},
		{"negative sequence", base + "/segments/-1", segmentBody(8), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, http.MethodPut, tt.path, tt.body, "application/octet-stream")
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestMeetingControlRejectsUnknownAction(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	resp := h.do(t, http.MethodPost, "/v1/meeting/"+started.MeetingID+"/control",
		strings.NewReader(`{"action":"selfdestruct"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStatusAdvertisesMeetingCapability(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodGet, "/v1/status", nil, "")
	var status map[string]any
	decodeJSON(t, resp, &status)
	if status["meetings"] != true {
		t.Fatalf("meetings capability = %v, want true", status["meetings"])
	}
	if status["protocol_version"] != float64(ProtocolVersion) {
		t.Fatalf("protocol_version = %v, want %d", status["protocol_version"], ProtocolVersion)
	}
}

// TestMeetingRoutesAbsentWithoutManager proves a serve built without meeting
// capture answers 404 and says so in /v1/status, as the protocol doc promises.
func TestMeetingRoutesAbsentWithoutManager(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	disp := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go disp.Run(context.Background())
	srv := New(Options{Bind: "127.0.0.1:0", Credentials: creds, Bus: bus, Dispatcher: disp})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	h := &meetingHarness{addr: waitAddr(t, srv), token: creds.Token, client: insecureHTTPClient()}

	resp := h.do(t, http.MethodPost, "/v1/meeting/start", strings.NewReader(`{}`), "application/json")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("start without manager = %d, want 404", resp.StatusCode)
	}
	resp = h.do(t, http.MethodGet, "/v1/status", nil, "")
	var status map[string]any
	decodeJSON(t, resp, &status)
	if status["meetings"] != false {
		t.Fatalf("meetings capability = %v, want false", status["meetings"])
	}
}

func decodeJSON(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}

// waitForState waits on the session's own completion signal rather than
// polling over HTTP: serve rate-limits at 30 requests per 10 s, and a poll
// loop would spend that budget racing the pipeline.
func waitForState(t *testing.T, h *meetingHarness, id string, want remote.State) remote.Status {
	t.Helper()
	session, err := h.manager.Session(id)
	if err != nil {
		t.Fatal(err)
	}
	if done := session.Done(); done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("processing did not finish")
		}
	}
	resp := h.do(t, http.MethodGet, "/v1/meeting/"+id, nil, "")
	var status remote.Status
	decodeJSON(t, resp, &status)
	if status.State != want {
		t.Fatalf("state = %q, want %q (error %q)", status.State, want, status.Error)
	}
	return status
}

// TestSegmentUploadsGetTheirOwnRateBudget is the resume path: a phone that was
// offline pushes its whole outbox at once. Under the shared 30-per-10s guard
// that client would be throttled into a stall — exactly the failure the outbox
// exists to prevent.
func TestSegmentUploadsGetTheirOwnRateBudget(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	base := "/v1/meeting/" + started.MeetingID

	const burst = 60 // twice the general per-IP allowance
	for seq := 0; seq < burst; seq++ {
		resp := h.do(t, http.MethodPut, base+"/segments/"+strconv.Itoa(seq), segmentBody(16), "application/octet-stream")
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusNoContent {
			t.Fatalf("segment %d = %d, want 204 (outbox drain must not be throttled)", seq, status)
		}
	}
}

// TestNonMeetingRoutesKeepTheGeneralLimit proves the carve-out is narrow: the
// rest of the surface still gets the abuse guard.
func TestNonMeetingRoutesKeepTheGeneralLimit(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	throttled := false
	for i := 0; i < 60; i++ {
		resp := h.do(t, http.MethodGet, "/v1/status", nil, "")
		status := resp.StatusCode
		resp.Body.Close()
		if status == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("/v1/status was never rate limited")
	}
}

func TestSegmentContentTypeIsChecked(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	path := "/v1/meeting/" + started.MeetingID + "/segments/0"

	tests := []struct {
		name        string
		contentType string
		want        int
	}{
		{"raw octet-stream", "application/octet-stream", http.StatusNoContent},
		{"octet-stream with parameters", "application/octet-stream; charset=binary", http.StatusNoContent},
		{"linear PCM", "audio/l16", http.StatusNoContent},
		{"unset is allowed", "", http.StatusNoContent},
		{"JSON posted by mistake", "application/json", http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, http.MethodPut, path, segmentBody(16), tt.contentType)
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestSegmentSequenceIsBounded(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	resp := h.do(t, http.MethodPut,
		"/v1/meeting/"+started.MeetingID+"/segments/1099511627776", segmentBody(16), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an out-of-range sequence", resp.StatusCode)
	}
}

func TestStopLastSeqIsBounded(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	resp := h.do(t, http.MethodPost, "/v1/meeting/"+started.MeetingID+"/stop",
		strings.NewReader(`{"last_seq":1099511627776}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an out-of-range last_seq", resp.StatusCode)
	}
}

// recordAndFinish drives a meeting to ready over the wire: one segment, stop,
// then poll status until the no-op pipeline publishes.
func (h *meetingHarness) recordAndFinish(t *testing.T) string {
	t.Helper()
	started := h.startMeeting(t)
	resp := h.do(t, http.MethodPut, "/v1/meeting/"+started.MeetingID+"/segments/0",
		segmentBody(1600), "application/octet-stream")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("segment status = %d", resp.StatusCode)
	}
	resp = h.do(t, http.MethodPost, "/v1/meeting/"+started.MeetingID+"/stop",
		strings.NewReader(`{"last_seq":0}`), "application/json")
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := h.do(t, http.MethodGet, "/v1/meeting/"+started.MeetingID, nil, "")
		var out remote.Status
		err := json.NewDecoder(status.Body).Decode(&out)
		status.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if out.State == remote.StateReady {
			return started.MeetingID
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("meeting never reached ready")
	return ""
}

// The route endpoint files once and answers retries from the receipt cache —
// the importer does not dedupe, so idempotency is serve's job.
func TestMeetingRouteFilesOnceAndCachesTheReceipt(t *testing.T) {
	var calls int32
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		if summary.Bundle == "" {
			t.Error("route func received a summary without a bundle path")
		}
		if campaign != "mytools" {
			t.Errorf("campaign = %q, want mytools", campaign)
		}
		return remote.RouteReceipt{Outcome: "routed", Destination: "mytools notes/meetings"}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	id := h.recordAndFinish(t)

	for i := 0; i < 2; i++ {
		resp := h.do(t, http.MethodPost, "/v1/meeting/"+id+"/route",
			strings.NewReader(`{"campaign":"mytools"}`), "application/json")
		var receipt remote.RouteReceipt
		if err := json.NewDecoder(resp.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("route #%d status = %d", i+1, resp.StatusCode)
		}
		if receipt.Destination != "mytools notes/meetings" {
			t.Fatalf("route #%d destination = %q", i+1, receipt.Destination)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("route func ran %d times for the same campaign, want exactly 1", got)
	}
}

func TestMeetingRouteBeforeReadyIsConflict(t *testing.T) {
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		t.Error("route func must not run for an unfinished meeting")
		return remote.RouteReceipt{}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	started := h.startMeeting(t)

	resp := h.do(t, http.MethodPost, "/v1/meeting/"+started.MeetingID+"/route",
		strings.NewReader(`{"campaign":"mytools"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("route while recording status = %d, want 409", resp.StatusCode)
	}
}

// 412 carries the CI0009 remediation to the phone verbatim.
func TestMeetingRouteOldCampIsPreconditionFailed(t *testing.T) {
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		return remote.RouteReceipt{}, fmt.Errorf("gate: %w", meeting.ErrImportMeetingUnsupported)
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	id := h.recordAndFinish(t)

	resp := h.do(t, http.MethodPost, "/v1/meeting/"+id+"/route",
		strings.NewReader(`{"campaign":"mytools"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("route status = %d, want 412", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "update camp") {
		t.Fatalf("412 body %q does not carry the remediation", raw)
	}
}

func TestMeetingRouteValidation(t *testing.T) {
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		return remote.RouteReceipt{Outcome: "routed"}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	id := h.recordAndFinish(t)

	resp := h.do(t, http.MethodPost, "/v1/meeting/"+id+"/route",
		strings.NewReader(`{"campaign":"  "}`), "application/json")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty campaign status = %d, want 400", resp.StatusCode)
	}

	// A serve wired without routing answers 503, not 404 — the surface
	// exists, the capability doesn't.
	bare := newMeetingHarness(t, remote.Options{})
	bareID := bare.recordAndFinish(t)
	resp = bare.do(t, http.MethodPost, "/v1/meeting/"+bareID+"/route",
		strings.NewReader(`{"campaign":"mytools"}`), "application/json")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured route status = %d, want 503", resp.StatusCode)
	}
}

// The reviewer's race: two simultaneous route calls (double tap, retry after
// a timed-out response) must share one importer execution, not file twice.
func TestMeetingRouteConcurrentRetriesFileOnce(t *testing.T) {
	var calls int32
	gate := make(chan struct{})
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		atomic.AddInt32(&calls, 1)
		<-gate // hold so both requests are provably concurrent
		return remote.RouteReceipt{Outcome: "routed", Destination: campaign + " notes/meetings"}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	id := h.recordAndFinish(t)

	const racers = 4
	status := make(chan int, racers)
	for range racers {
		go func() {
			resp := h.do(t, http.MethodPost, "/v1/meeting/"+id+"/route",
				strings.NewReader(`{"campaign":"mytools"}`), "application/json")
			resp.Body.Close()
			status <- resp.StatusCode
		}()
	}
	// Give every request time to reach the handler before releasing the sink.
	time.Sleep(150 * time.Millisecond)
	close(gate)
	for range racers {
		if code := <-status; code != http.StatusOK {
			t.Fatalf("concurrent route status = %d, want 200", code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("importer executed %d times for %d concurrent requests, want exactly 1", got, racers)
	}
}

// Capture modes are distinct routes: a meeting-import receipt must not answer
// a later intent/note capture for the same campaign.
func TestMeetingRouteCaptureModesRouteSeparately(t *testing.T) {
	var captures []string
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		captures = append(captures, capture)
		return remote.RouteReceipt{Outcome: "routed"}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{}, route)
	id := h.recordAndFinish(t)

	for _, body := range []string{
		`{"campaign":"mytools"}`,
		`{"campaign":"mytools","capture":"intent"}`,
		`{"campaign":"mytools","capture":"import-meeting"}`, // normalizes to meeting → cache hit
	} {
		resp := h.do(t, http.MethodPost, "/v1/meeting/"+id+"/route", strings.NewReader(body), "application/json")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("route %s status = %d", body, resp.StatusCode)
		}
	}
	if len(captures) != 2 || captures[0] != "" || captures[1] != "intent" {
		t.Fatalf("importer captures = %q, want [\"\" \"intent\"] (third call is a normalized cache hit)", captures)
	}
}

// The document endpoint serves the canonical meeting.md — the phone's
// speaker-labeled result view renders exactly what the Mac wrote.
func TestMeetingDocumentServedAfterReady(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)

	base := "/v1/meeting/" + started.MeetingID

	// Not ready yet: the meeting is still recording.
	resp := h.do(t, http.MethodGet, base+"/document", nil, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("document while recording = %d, want 409", resp.StatusCode)
	}

	resp = h.do(t, http.MethodPut, base+"/segments/0", segmentBody(1600), "application/octet-stream")
	resp.Body.Close()
	resp = h.do(t, http.MethodPost, base+"/stop", strings.NewReader(`{"last_seq":0}`), "application/json")
	resp.Body.Close()
	waitForState(t, h, started.MeetingID, remote.StateReady)

	resp = h.do(t, http.MethodGet, base+"/document", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("document status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content type = %q", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "Standup") {
		t.Fatalf("document does not carry the meeting content:\n%s", raw)
	}
}
