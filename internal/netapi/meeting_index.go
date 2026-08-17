package netapi

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// GET /v1/meetings is the history surface (PROTOCOL_DELTAS D7). It reads the
// bundles on disk — which outlive the process — and overlays whatever this
// serve happens to be recording right now, so one response answers both "what
// did I record" and "what is recording".

// meetingIndexEntry is a disk entry plus the live id, when this process owns
// the recording. The CLI's `meeting list` emits the same shape without it.
type meetingIndexEntry struct {
	meeting.BundleEntry
	LiveID string `json:"live_id,omitempty"`
}

// meetingIndexResponse is the GET /v1/meetings body.
type meetingIndexResponse struct {
	MeetingsDir string              `json:"meetings_dir"`
	Count       int                 `json:"count"`
	Truncated   bool                `json:"truncated"`
	Meetings    []meetingIndexEntry `json:"meetings"`
}

func (s *Server) handleMeetingIndex(w http.ResponseWriter, r *http.Request) {
	manager := s.meetings
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "meeting capture is not configured"})
		return
	}
	opts, ok := parseIndexOptions(w, r)
	if !ok {
		return
	}
	entries, truncated, err := manager.Index(r.Context(), opts)
	if err != nil {
		// A missing meetings dir is an empty list; only a dir that exists and
		// cannot be read gets here.
		log.Printf("netapi: meeting index failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "meeting history unavailable"})
		return
	}
	meetings := overlayLiveSessions(entries, manager.Sessions())
	writeJSON(w, http.StatusOK, meetingIndexResponse{
		MeetingsDir: manager.Root(),
		Count:       len(meetings),
		Truncated:   truncated,
		Meetings:    meetings,
	})
}

// parseIndexOptions reads the query bounds, writing the 400 itself when a
// client sends something it cannot mean.
func parseIndexOptions(w http.ResponseWriter, r *http.Request) (meeting.IndexOptions, bool) {
	var opts meeting.IndexOptions
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return opts, false
		}
		opts.Limit = limit
	}
	if raw := r.URL.Query().Get("since"); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since"})
			return opts, false
		}
		opts.Since = since
	}
	return opts, true
}

// overlayLiveSessions merges this process's in-memory meetings over the disk
// index, matched by bundle path. Disk cannot know a meeting is recording — the
// trailer is only written at the end — so the session is authoritative for
// state while it holds one, and the disk entry is authoritative once it does
// not.
func overlayLiveSessions(entries []meeting.BundleEntry, sessions []*remote.Session) []meetingIndexEntry {
	live := make(map[string]*remote.Session, len(sessions))
	for _, session := range sessions {
		live[session.BundlePath()] = session
	}
	out := make([]meetingIndexEntry, 0, len(entries))
	for _, entry := range entries {
		row := meetingIndexEntry{BundleEntry: entry}
		if session, ok := live[entry.Bundle]; ok {
			row.LiveID = session.ID()
			switch state := session.Status().State; state {
			case remote.StateRecording, remote.StateProcessing:
				row.State = string(state)
			}
		}
		out = append(out, row)
	}
	return out
}
