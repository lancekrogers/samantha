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
