package netapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// recordOneMeeting drives a meeting from start to ready and reports the live
// id and the bundle id a client would have persisted from its first status.
func recordOneMeeting(t *testing.T, h *meetingHarness) (liveID, bundleID string) {
	t.Helper()
	started := h.startMeeting(t)
	resp := h.do(t, http.MethodPost, "/v1/meeting/"+started.MeetingID+"/stop",
		strings.NewReader(`{"last_seq":-1}`), "application/json")
	var status remote.Status
	decodeJSON(t, resp, &status)
	final := waitForState(t, h, started.MeetingID, remote.StateReady)
	if final.Bundle == "" {
		t.Fatal("status carried no bundle path to derive a bundle id from")
	}
	return started.MeetingID, filepath.Base(final.Bundle)
}

func fetchIndex(t *testing.T, h *meetingHarness, query string) meetingIndexResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/v1/meetings"+query, nil, "")
	var index meetingIndexResponse
	decodeJSON(t, resp, &index)
	return index
}

func TestMeetingIndexListsFinishedAndLiveMeetings(t *testing.T) {
	root := t.TempDir()
	h := newMeetingHarness(t, remote.Options{Root: root})

	empty := fetchIndex(t, h, "")
	if empty.MeetingsDir != root || empty.Count != 0 || empty.Truncated {
		t.Fatalf("empty index = %+v, want an empty history for %s", empty, root)
	}
	if empty.Meetings == nil {
		t.Error("meetings is null, want an empty array")
	}

	_, bundleID := recordOneMeeting(t, h)
	live := h.startMeeting(t)

	index := fetchIndex(t, h, "")
	if index.Count != 2 {
		t.Fatalf("count = %d, want the finished and the live meeting", index.Count)
	}
	byID := make(map[string]meetingIndexEntry, len(index.Meetings))
	for _, entry := range index.Meetings {
		byID[entry.ID] = entry
	}
	finished, ok := byID[bundleID]
	if !ok {
		t.Fatalf("finished bundle %q missing from %+v", bundleID, index.Meetings)
	}
	if finished.State != meeting.BundleStateReady {
		t.Errorf("finished state = %q, want ready", finished.State)
	}
	if finished.Description != "Standup" || finished.Document == "" || finished.EventsFile == "" {
		t.Errorf("finished entry = %+v, want the recorder's description and paths", finished)
	}

	// The live meeting has no trailer on disk, so only the session can say it
	// is recording — that overlay is the point of the route.
	liveBundle := filepath.Base(mustSession(t, h, live.MeetingID).BundlePath())
	recording, ok := byID[liveBundle]
	if !ok {
		t.Fatalf("live bundle %q missing from %+v", liveBundle, index.Meetings)
	}
	if recording.LiveID != live.MeetingID {
		t.Fatalf("live_id = %q, want %q", recording.LiveID, live.MeetingID)
	}
	if recording.State != string(remote.StateRecording) {
		t.Errorf("live state = %q, want recording", recording.State)
	}
}

// TestMeetingIndexRequiresAuth pins that history is behind the bearer token
// like every other /v1 route: a meetings list names people and topics.
func TestMeetingIndexRequiresAuth(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	req, err := http.NewRequest(http.MethodGet, "https://"+h.addr+"/v1/meetings", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", resp.StatusCode)
	}
}

func TestMeetingIndexRejectsUnusableQueries(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"non-numeric limit", "?limit=all", "invalid limit"},
		{"zero limit", "?limit=0", "invalid limit"},
		{"negative limit", "?limit=-3", "invalid limit"},
		{"unparseable since", "?since=yesterday", "invalid since"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, http.MethodGet, "/v1/meetings"+tt.query, nil, "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var problem map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem["error"] != tt.want {
				t.Fatalf("error = %q, want %q", problem["error"], tt.want)
			}
		})
	}
}

func TestMeetingIndexHonoursLimitAndSince(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	recordOneMeeting(t, h)
	recordOneMeeting(t, h)

	limited := fetchIndex(t, h, "?limit=1")
	if limited.Count != 1 || !limited.Truncated {
		t.Fatalf("limited index = count %d truncated %v, want 1 and true", limited.Count, limited.Truncated)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	none := fetchIndex(t, h, "?since="+url.QueryEscape(future))
	if none.Count != 0 || none.Truncated {
		t.Fatalf("future since = count %d truncated %v, want an empty list", none.Count, none.Truncated)
	}
}

// TestMeetingIndexIsAbsentWithoutCapture pins the registration contract: a
// serve built without meeting capture does not answer the history route at
// all, so a client can tell "no capture here" from "no meetings yet".
func TestMeetingIndexIsAbsentWithoutCapture(t *testing.T) {
	_, addr, creds := startTestServer(t, &scriptedRunner{}, events.NewBus())
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/v1/meetings", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := insecureHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when meeting capture is not configured", resp.StatusCode)
	}
}

// TestBundleIDResolvesAfterRestart is G33 on the wire: a second serve over the
// same meetings dir — which is what a restart is — answers for the bundle id
// the client stored, while the live id from the dead process is gone.
func TestBundleIDResolvesAfterRestart(t *testing.T) {
	root := t.TempDir()
	first := newMeetingHarness(t, remote.Options{Root: root})
	liveID, bundleID := recordOneMeeting(t, first)

	restarted := newMeetingHarness(t, remote.Options{Root: root})

	resp := restarted.do(t, http.MethodGet, "/v1/meeting/"+liveID, nil, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stale live id = %d, want 404", resp.StatusCode)
	}

	var status remote.Status
	resp = restarted.do(t, http.MethodGet, "/v1/meeting/"+bundleID, nil, "")
	decodeJSON(t, resp, &status)
	if status.MeetingID != bundleID {
		t.Errorf("meeting_id = %q, want the bundle id %q", status.MeetingID, bundleID)
	}
	if status.State != remote.StateReady {
		t.Errorf("state = %q, want ready", status.State)
	}
	if status.Bundle != filepath.Join(root, bundleID) {
		t.Errorf("bundle = %q, want %q", status.Bundle, filepath.Join(root, bundleID))
	}
	if status.Result == nil || status.Result.Description != "Standup" {
		t.Errorf("result = %+v, want the summary rebuilt from disk", status.Result)
	}
	if len(status.MissingSeqs) != 0 || status.Step != "" {
		t.Errorf("status = %+v, want no missing seqs and no step for a finished bundle", status)
	}

	document := restarted.do(t, http.MethodGet, "/v1/meeting/"+bundleID+"/document", nil, "")
	defer document.Body.Close()
	if document.StatusCode != http.StatusOK {
		t.Fatalf("document status = %d, want 200", document.StatusCode)
	}
	if ct := document.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type = %q, want text/markdown", ct)
	}
	raw, _ := io.ReadAll(document.Body)
	if !strings.Contains(string(raw), "# Meeting: Standup") {
		t.Errorf("document = %q, want the bundle's meeting.md", raw)
	}
}

// TestMeetingIDsThatCouldEscapeTheMeetingsDirAreRefused proves a hostile id is
// rejected on shape, before it is ever joined onto meetings_dir.
func TestMeetingIDsThatCouldEscapeTheMeetingsDirAreRefused(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	tests := []struct {
		name string
		id   string
	}{
		{"encoded traversal", "..%2F..%2Fetc%2Fpasswd"},
		{"absolute path", "%2Fetc%2Fpasswd"},
		{"hidden name", ".hidden.meeting"},
		{"space in the id", "weekly%20sync.meeting"},
		{"absurdly long", strings.Repeat("a", 200) + ".meeting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, http.MethodGet, "/v1/meeting/"+tt.id, nil, "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, raw)
			}
			var problem map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem["error"] != "meeting: invalid meeting id" {
				t.Fatalf("error = %q, want the invalid-id message", problem["error"])
			}
		})
	}
}

// mustSession looks a live session up on the harness's manager.
func mustSession(t *testing.T, h *meetingHarness, id string) *remote.Session {
	t.Helper()
	session, err := h.manager.Session(id)
	if err != nil {
		t.Fatalf("Session(%q) error = %v", id, err)
	}
	return session
}

func TestUnknownBundleIDIs404(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodGet, "/v1/meeting/never-recorded-20260816-101500.meeting", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestWritePathsRejectBundleIDs is the heart of the two-id contract: a
// finished bundle is a real id for reading and routing, and never takes audio
// or control events again.
func TestWritePathsRejectBundleIDs(t *testing.T) {
	root := t.TempDir()
	first := newMeetingHarness(t, remote.Options{Root: root})
	_, bundleID := recordOneMeeting(t, first)
	h := newMeetingHarness(t, remote.Options{Root: root})
	base := "/v1/meeting/" + bundleID

	tests := []struct {
		name        string
		method      string
		path        string
		body        io.Reader
		contentType string
	}{
		{"segment upload", http.MethodPut, base + "/segments/0", segmentBody(80), "application/octet-stream"},
		{"control", http.MethodPost, base + "/control", strings.NewReader(`{"action":"note","text":"late"}`), "application/json"},
		{"stop", http.MethodPost, base + "/stop", strings.NewReader(`{"last_seq":0}`), "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, tt.method, tt.path, tt.body, tt.contentType)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 409 (body %s)", resp.StatusCode, raw)
			}
		})
	}
}

func TestBundleDocumentIsConflictUntilNotesExist(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "notesless-20260816-101500.meeting")
	writer, err := meetinglog.CreateBundle(bundle, "Notesless", "test", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(bundle, meetinglog.BundleDocumentName)); err != nil {
		t.Fatal(err)
	}

	h := newMeetingHarness(t, remote.Options{Root: root})
	resp := h.do(t, http.MethodGet, "/v1/meeting/"+filepath.Base(bundle)+"/document", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var problem map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["error"] != "meeting: notes are not ready yet" {
		t.Fatalf("error = %q", problem["error"])
	}
}

// TestRoutingAFinishedBundleFilesOnceThen409s covers the recovery path the Mac
// depends on: the agent restarted, the session is gone, and the bundle still
// has to reach the campaign — exactly once.
func TestRoutingAFinishedBundleFilesOnceThen409s(t *testing.T) {
	root := t.TempDir()
	first := newMeetingHarness(t, remote.Options{Root: root})
	_, bundleID := recordOneMeeting(t, first)

	var calls int
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		calls++
		// Mirror the router: a delivered note leaves durable provenance, which
		// is the only thing a restarted serve can dedupe against.
		if err := meeting.AppendRoutedEvent(summary.JSONLFile, meeting.Receipt{
			DestinationID: "camp:" + campaign, Type: meeting.TypeCampaign,
			Outcome: meeting.OutcomeRouted, Detail: "notes/meetings/x.md",
		}); err != nil {
			t.Errorf("append routed: %v", err)
		}
		return remote.RouteReceipt{Outcome: meeting.OutcomeRouted, Detail: "notes/meetings/x.md",
			Destination: campaign + " notes/meetings"}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{Root: root}, route)
	body := `{"campaign":"blockhead"}`

	var receipt remote.RouteReceipt
	resp := h.do(t, http.MethodPost, "/v1/meeting/"+bundleID+"/route", strings.NewReader(body), "application/json")
	decodeJSON(t, resp, &receipt)
	if receipt.Outcome != meeting.OutcomeRouted {
		t.Fatalf("receipt = %+v, want routed", receipt)
	}

	resp = h.do(t, http.MethodPost, "/v1/meeting/"+bundleID+"/route", strings.NewReader(body), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("second route = %d, want 409 (body %s)", resp.StatusCode, raw)
	}
	var problem map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(problem["error"], "already routed to camp:blockhead") {
		t.Fatalf("error = %q, want it to name where the notes went", problem["error"])
	}
	if calls != 1 {
		t.Fatalf("route func ran %d times, want 1", calls)
	}
}

func TestProtocolVersionAdvertisesHistory(t *testing.T) {
	if ProtocolVersion != 3 {
		t.Fatalf("ProtocolVersion = %d, want 3 for meeting history (D7)", ProtocolVersion)
	}
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodGet, "/v1/status", nil, "")
	var status map[string]any
	decodeJSON(t, resp, &status)
	if status["protocol_version"] != float64(3) {
		t.Fatalf("protocol_version = %v, want 3", status["protocol_version"])
	}
}

// TestBundleIDOfARecordingMeetingAnswersFromTheSession closes the hole a
// bundle id opens while a meeting is in flight: on disk it looks like an
// interrupted, finished meeting, so reading or routing it by bundle id would
// otherwise report — and file — notes that do not exist yet.
func TestBundleIDOfARecordingMeetingAnswersFromTheSession(t *testing.T) {
	root := t.TempDir()
	routed := 0
	route := func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		routed++
		return remote.RouteReceipt{Outcome: meeting.OutcomeRouted}, nil
	}
	h := newRoutedMeetingHarness(t, remote.Options{Root: root}, route)
	live := h.startMeeting(t)
	bundleID := filepath.Base(mustSession(t, h, live.MeetingID).BundlePath())

	var status remote.Status
	resp := h.do(t, http.MethodGet, "/v1/meeting/"+bundleID, nil, "")
	decodeJSON(t, resp, &status)
	if status.State != remote.StateRecording {
		t.Fatalf("state = %q, want recording — the bundle has no trailer yet", status.State)
	}

	resp = h.do(t, http.MethodPost, "/v1/meeting/"+bundleID+"/route",
		strings.NewReader(`{"campaign":"blockhead"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("route while recording = %d, want 409 (body %s)", resp.StatusCode, raw)
	}
	if routed != 0 {
		t.Fatalf("a meeting still recording was filed %d time(s)", routed)
	}

	// Capture still belongs to the live id: a bundle id never takes audio.
	resp = h.do(t, http.MethodPut, "/v1/meeting/"+bundleID+"/segments/0", segmentBody(80), "application/octet-stream")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("segment by bundle id = %d, want 409", resp.StatusCode)
	}
}
