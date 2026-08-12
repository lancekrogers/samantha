package netapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

func TestMeetingStartAcceptsWatchSource(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodPost, "/v1/meeting/start",
		strings.NewReader(`{"title":"Standup","source":"watch"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start with source=watch = %d, want 201", resp.StatusCode)
	}
}

func TestMeetingStartRejectsUnknownSource(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	resp := h.do(t, http.MethodPost, "/v1/meeting/start",
		strings.NewReader(`{"source":"quest"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("start with unknown source = %d, want 400", resp.StatusCode)
	}
}
