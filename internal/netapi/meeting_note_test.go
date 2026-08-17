package netapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// TestMeetingNoteControlOverTheWire pins the D7 note action: the error cases
// answer 400 with the reason the UI shows, and the accepted note comes back as
// an ordinary Status.
func TestMeetingNoteControlOverTheWire(t *testing.T) {
	h := newMeetingHarness(t, remote.Options{})
	started := h.startMeeting(t)
	path := "/v1/meeting/" + started.MeetingID + "/control"

	tests := []struct {
		name      string
		body      string
		want      int
		wantError string
	}{
		{
			name:      "empty text",
			body:      `{"action":"note","offset_ms":1000}`,
			want:      http.StatusBadRequest,
			wantError: "meeting: note requires text",
		},
		{
			name:      "whitespace text",
			body:      `{"action":"note","offset_ms":1000,"text":"   "}`,
			want:      http.StatusBadRequest,
			wantError: "meeting: note requires text",
		},
		{
			name: "accepted",
			body: `{"action":"note","offset_ms":184300,"text":"decide the pricing tier next week"}`,
			want: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.do(t, http.MethodPost, path, strings.NewReader(tt.body), "application/json")
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tt.want, raw)
			}
			if tt.wantError != "" {
				var problem map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
					t.Fatal(err)
				}
				if problem["error"] != tt.wantError {
					t.Fatalf("error = %q, want %q", problem["error"], tt.wantError)
				}
				return
			}
			var status remote.Status
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				t.Fatal(err)
			}
			if status.State != remote.StateRecording {
				t.Fatalf("state = %q, want recording", status.State)
			}
		})
	}
}
