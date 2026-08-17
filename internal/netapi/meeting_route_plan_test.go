package netapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// TestMeetingStartRoutePlanValidation pins the shape checks at start: the body
// text is what a client shows the user, and an unresolvable destination is
// deliberately NOT rejected here — that costs a camp subprocess on the request
// path and must fail loudly at delivery instead of blocking a recording.
func TestMeetingStartRoutePlanValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      int
		wantError string
	}{
		{
			name:      "empty destination",
			body:      `{"title":"Sync","route_plan":{"destination_id":""}}`,
			want:      http.StatusBadRequest,
			wantError: "meeting: route_plan requires destination_id",
		},
		{
			name: "unknown body scope",
			body: `{"title":"Sync","route_plan":{"destination_id":"camp:blockhead","body":"summary"}}`,
			want: http.StatusBadRequest,
		},
		{
			name: "accepted",
			body: `{"title":"Sync","source":"mac","route_plan":{"destination_id":"camp:blockhead","body":"full"}}`,
			want: http.StatusCreated,
		},
		{
			name: "accepted with the configured body",
			body: `{"title":"Sync","route_plan":{"destination_id":"docs"}}`,
			want: http.StatusCreated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMeetingHarness(t, remote.Options{})
			resp := h.do(t, http.MethodPost, "/v1/meeting/start", strings.NewReader(tt.body), "application/json")
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tt.want, raw)
			}
			if tt.wantError == "" {
				return
			}
			var problem map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem["error"] != tt.wantError {
				t.Fatalf("error = %q, want %q", problem["error"], tt.wantError)
			}
		})
	}
}
