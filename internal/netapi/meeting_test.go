package netapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
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
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  disp,
		Meetings:    manager,
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
