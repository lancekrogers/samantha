package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// startSessionDeleteServer boots a server whose only extra wiring is the
// session-delete callback, so a nil one can be tested as its own case (the
// route must then be entirely absent, not merely erroring).
func startSessionDeleteServer(t *testing.T, del func(id string) error) (string, *Credentials) {
	t.Helper()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	d := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	s := New(Options{
		Bind:          "127.0.0.1:0",
		Credentials:   creds,
		Bus:           bus,
		Dispatcher:    d,
		DeleteSession: del,
	})
	go func() {
		if err := s.ListenAndServe(ctx); err != nil {
			t.Errorf("ListenAndServe: %v", err)
		}
	}()
	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			cancel()
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s.Addr().String(), creds
}

func deleteSession(t *testing.T, addr, token, id string) (int, map[string]any) {
	t.Helper()
	url := "https://" + addr + "/v1/sessions/" + id
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := insecureClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestSessionDeleteSucceeds(t *testing.T) {
	var gotID string
	addr, creds := startSessionDeleteServer(t, func(id string) error {
		gotID = id
		return nil
	})

	status, body := deleteSession(t, addr, creds.Token, "20260816-231455-a3f9")
	if status != http.StatusOK {
		t.Fatalf("DELETE /v1/sessions/{id} = %d, want 200", status)
	}
	if body["deleted"] != "20260816-231455-a3f9" {
		t.Fatalf("body = %v, want deleted to name the id", body)
	}
	if gotID != "20260816-231455-a3f9" {
		t.Fatalf("callback received id = %q", gotID)
	}
}

func TestSessionDeleteNotFound(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, func(id string) error {
		return ErrSessionNotFound
	})

	status, body := deleteSession(t, addr, creds.Token, "does-not-exist")
	if status != http.StatusNotFound {
		t.Fatalf("DELETE /v1/sessions/{id} = %d, want 404", status)
	}
	if body["error"] != ErrSessionNotFound.Error() {
		t.Fatalf("error = %v, want the sentinel's message", body["error"])
	}
}

// An unexpected failure (permissions, disk I/O, a malformed id — anything
// that is not ErrSessionActive or ErrSessionNotFound) must not be
// mislabeled as 404: a client would stop asking when the real problem is
// server-side. Found by adversarial review: the first cut of this handler
// mapped every non-active error to 404, including this case.
func TestSessionDeleteUnexpectedErrorIsServerError(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, func(id string) error {
		return errors.New("delete session foo: remove /var/.../foo.json: permission denied")
	})

	status, body := deleteSession(t, addr, creds.Token, "20260816-231455-a3f9")
	if status != http.StatusInternalServerError {
		t.Fatalf("DELETE /v1/sessions/{id} = %d, want 500 for an unexpected callback error", status)
	}
	if body["error"] != "delete session foo: remove /var/.../foo.json: permission denied" {
		t.Fatalf("error = %v, want the callback's message surfaced", body["error"])
	}
}

func TestSessionDeleteActiveSessionConflicts(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, func(id string) error {
		return ErrSessionActive
	})

	status, body := deleteSession(t, addr, creds.Token, "20260816-231455-a3f9")
	if status != http.StatusConflict {
		t.Fatalf("DELETE /v1/sessions/{id} = %d, want 409", status)
	}
	if body["error"] != "session is active" {
		t.Fatalf("error = %v, want \"session is active\"", body["error"])
	}
}

// Route registration happens at the mux level: a bare DELETE to the parent
// path with no {id} segment does not match the pattern at all (404 from the
// mux itself), so the missing-id 400 is exercised through the handler
// directly instead.
func TestSessionDeleteMissingIDIsBadRequest(t *testing.T) {
	s := &Server{opts: Options{DeleteSession: func(string) error { return nil }}}
	req, err := http.NewRequest(http.MethodDelete, "https://example/v1/sessions/", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleSessionDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestSessionDeleteRouteAbsentWithoutCallback(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, nil)

	status, _ := deleteSession(t, addr, creds.Token, "20260816-231455-a3f9")
	if status != http.StatusNotFound {
		t.Fatalf("DELETE /v1/sessions/{id} = %d, want 404 when DeleteSession is nil (route unregistered)", status)
	}
}

func TestSessionDeleteRequiresBearer(t *testing.T) {
	addr, _ := startSessionDeleteServer(t, func(id string) error { return nil })

	status, _ := deleteSession(t, addr, "", "20260816-231455-a3f9")
	if status != http.StatusUnauthorized {
		t.Fatalf("DELETE /v1/sessions/{id} without a token = %d, want 401", status)
	}
}

// TestSessionDeleteRejectsUnsafeIDs is M2 from the 006/01 acceptance run: a
// traversing or malformed id is a client error, not a server error, and it must
// be refused on shape before the store is asked for anything.
//
// The ids are written as they appear in the request line. `..` and `/` are sent
// percent-encoded on purpose: a literal one is rewritten by ServeMux's path
// cleaning and never reaches the handler, which is exactly why the encoded form
// was the one that got through and produced the 500.
func TestSessionDeleteRejectsUnsafeIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"the acceptance run's traversal", "..%2Fetc%2Fpasswd"},
		{"encoded dot-dot", "%2e%2e"},
		{"encoded traversal out of the sessions dir", "%2e%2e%2f%2e%2e%2fetc%2fpasswd"},
		{"absolute path", "%2Fetc%2Fpasswd"},
		{"windows separator", "..%5Cwindows%5Csystem32"},
		{"hidden name", ".ssh"},
		{"embedded null", "abc%00def"},
		{"whitespace", "not%20an%20id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var asked string
			addr, creds := startSessionDeleteServer(t, func(id string) error {
				asked = id
				return nil
			})

			status, body := deleteSession(t, addr, creds.Token, tt.id)
			if status != http.StatusBadRequest {
				t.Fatalf("DELETE /v1/sessions/%s = %d, want 400", tt.id, status)
			}
			if body["error"] != "session: invalid session id" {
				t.Errorf("error = %v, want the invalid-id message", body["error"])
			}
			if asked != "" {
				t.Errorf("the store was asked to delete %q; an unsafe id must never reach it", asked)
			}
		})
	}
}

// TestSessionDeleteAcceptsRealIDShapes is the other half of M2: tightening the
// guard must not start refusing the ids serve actually mints.
func TestSessionDeleteAcceptsRealIDShapes(t *testing.T) {
	for _, id := range []string{
		"20260816-231455-a3f9c1d2",
		"20260816-231455-a3f9c1d2.pre-compact",
		"a",
		"session_1",
	} {
		t.Run(id, func(t *testing.T) {
			addr, creds := startSessionDeleteServer(t, func(string) error { return nil })
			if status, _ := deleteSession(t, addr, creds.Token, id); status != http.StatusOK {
				t.Fatalf("DELETE /v1/sessions/%s = %d, want 200", id, status)
			}
		})
	}
}

// A literal `..` never reaches the handler through a client — ServeMux cleans
// the path and answers 301 — so the handler is driven directly to prove it
// would refuse one anyway.
func TestSessionDeleteHandlerRefusesLiteralDotDot(t *testing.T) {
	var asked string
	s := &Server{opts: Options{DeleteSession: func(id string) error { asked = id; return nil }}}
	req, err := http.NewRequest(http.MethodDelete, "https://example/v1/sessions/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetPathValue("id", "../../etc/passwd")
	rec := httptest.NewRecorder()
	s.handleSessionDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if asked != "" {
		t.Fatalf("the store was asked to delete %q", asked)
	}
}

// The resume route reads the same {id} into the same dispatcher, so it carries
// the same guard: one unsafe-id rule for the whole /v1/sessions/{id} surface.
func TestSessionResumeRejectsUnsafeIDs(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, func(string) error { return nil })
	url := "https://" + addr + "/v1/sessions/..%2Fetc%2Fpasswd/resume"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := insecureClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /v1/sessions/{traversal}/resume = %d, want 400", resp.StatusCode)
	}
}

// TestSessionDeleteTraversalIsNotAServerError reproduces the exact chain the
// acceptance run hit: the id reached the store, the store refused it with a
// plain error, and the handler's default branch reported that as 500. The
// callback here returns the store's real message, so this test fails with 500
// against the pre-fix handler and passes only because the edge refuses the id
// before the callback is reached at all.
func TestSessionDeleteTraversalIsNotAServerError(t *testing.T) {
	addr, creds := startSessionDeleteServer(t, func(id string) error {
		return errors.New(`session: invalid id "../etc/passwd"`)
	})

	status, body := deleteSession(t, addr, creds.Token, "..%2Fetc%2Fpasswd")
	if status != http.StatusInternalServerError && status != http.StatusBadRequest {
		t.Fatalf("unexpected status %d", status)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("DELETE /v1/sessions/..%%2Fetc%%2Fpasswd = %d, want 400 (M2 of the acceptance run: it was 500)", status)
	}
	if body["error"] != "session: invalid session id" {
		t.Errorf("error = %v, want the invalid-id message, not the store's", body["error"])
	}
}
