package netapi

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// Meeting ids come in two shapes and a client holds both (PROTOCOL_DELTAS D7).
// A live id names an in-memory session and dies with the serve process; a
// bundle id is the directory name and lasts as long as the recording does.
// Everything that writes — segments, control, stop — needs a live session.
// Everything that reads, plus routing, works from either.

// meetingTarget is what a path id resolved to.
type meetingTarget struct {
	session *remote.Session
	manager *remote.Manager
	// bundle is set only when the id named a meeting on disk.
	bundle meeting.BundleEntry
	// byBundleID records that the path named the bundle rather than the live
	// session. Reads and routes then follow whatever the bundle is — including
	// a session still recording into it — but audio and control never do: a
	// segment belongs to the recording that produced it, and that recording is
	// addressed by its live id.
	byBundleID bool
}

// isLive reports that a session is answering for this id, whether the client
// named the live id or the bundle this process is still recording.
func (t meetingTarget) isLive() bool { return t.session != nil }

// acceptsCapture reports whether audio and control events may be appended.
func (t meetingTarget) acceptsCapture() bool { return t.session != nil && !t.byBundleID }

// resolveMeeting turns the path's id into a live session or a finished bundle,
// writing the error response itself when it can be neither.
func (s *Server) resolveMeeting(w http.ResponseWriter, r *http.Request) (meetingTarget, bool) {
	manager := s.meetings
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "meeting capture is not configured"})
		return meetingTarget{}, false
	}
	id := r.PathValue("id")
	if !meeting.SafeMeetingID(id) {
		// Refused on shape, before the id is ever joined onto meetings_dir:
		// path traversal must not reach the filesystem to be rejected.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "meeting: invalid meeting id"})
		return meetingTarget{}, false
	}
	if session, err := manager.Session(id); err == nil {
		return meetingTarget{session: session, manager: manager}, true
	}
	if entry, ok := manager.Bundle(r.Context(), id); ok {
		target := meetingTarget{manager: manager, bundle: entry, byBundleID: true}
		// A bundle this process is still recording only looks finished on
		// disk: no trailer, no notes, no summary. Answer from the session so a
		// client that only kept the bundle id cannot read or route a meeting
		// that has not happened yet.
		if session, live := manager.SessionForBundle(entry.Bundle); live {
			target.session = session
		}
		return target, true
	}
	// A live id from before a restart lands here: the session died with the
	// process, and the client's recovery is the bundle id it stored.
	writeMeetingError(w, remote.ErrNotFound)
	return meetingTarget{}, false
}

// bundleStatus answers for a meeting that exists only on disk: no segments
// left to miss, no pipeline step to report, and the summary rebuilt from the
// bundle's own events rather than from a session that no longer exists.
func bundleStatus(entry meeting.BundleEntry) remote.Status {
	summary := entry.Summary()
	return remote.Status{
		MeetingID: entry.ID,
		State:     remote.State(entry.State),
		Bundle:    entry.Bundle,
		Title:     entry.Description,
		StartedAt: entry.StartedAt,
		Result:    &summary,
	}
}

// writeBundleDocument serves a finished bundle's canonical meeting.md. A
// bundle whose document is not written yet is not an error — it is the same
// "notes are not ready" the live path reports.
func writeBundleDocument(w http.ResponseWriter, entry meeting.BundleEntry) {
	document, err := os.ReadFile(filepath.Join(entry.Bundle, meetinglog.BundleDocumentName))
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "meeting: notes are not ready yet"})
			return
		}
		log.Printf("netapi: meeting document read failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "meeting document unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}

// routeBundle files a finished meeting whose session is gone. Idempotency is
// the bundle's own routed event: once the notes are filed, filing them again
// is a mistake worth an error rather than a second note in the campaign.
func (s *Server) routeBundle(w http.ResponseWriter, r *http.Request, entry meeting.BundleEntry, req remote.RouteRequest, campaign string) {
	if entry.Route != nil && entry.Route.Status == meeting.RouteStatusRouted {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("meeting: meeting was already routed to %s", routedDestination(entry)),
		})
		return
	}
	if entry.State != meeting.BundleStateReady && entry.State != meeting.BundleStateInterrupted {
		writeMeetingError(w, remote.ErrNotRoutable)
		return
	}
	key := entry.Bundle + "\x00" + remote.CampaignRouteKey(req.Capture, campaign)
	receipt, err := s.bundleRoutes.once(key, func() (remote.RouteReceipt, error) {
		return s.routeMeeting(r.Context(), entry.Summary(), campaign, req.Capture)
	})
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// routedDestination names where the notes already went, for the 409 text.
func routedDestination(entry meeting.BundleEntry) string {
	if entry.Route.DestinationID != "" {
		return entry.Route.DestinationID
	}
	if entry.Route.Detail != "" {
		return entry.Route.Detail
	}
	return entry.ID
}

// routeGate single-flights route executions across requests. A finished bundle
// has no session to hold a receipt cache, and `camp idea notes import-meeting`
// does not dedupe, so two taps arriving together must share one execution
// instead of filing the meeting twice. Nothing is cached past completion: the
// durable routed event is what stops the third tap.
type routeGate struct {
	mu    sync.Mutex
	calls map[string]*routeAttempt
}

type routeAttempt struct {
	done    chan struct{}
	receipt remote.RouteReceipt
	err     error
}

func newRouteGate() *routeGate { return &routeGate{calls: make(map[string]*routeAttempt)} }

func (g *routeGate) once(key string, fn func() (remote.RouteReceipt, error)) (remote.RouteReceipt, error) {
	g.mu.Lock()
	if attempt, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-attempt.done
		return attempt.receipt, attempt.err
	}
	attempt := &routeAttempt{done: make(chan struct{})}
	g.calls[key] = attempt
	g.mu.Unlock()

	attempt.receipt, attempt.err = fn()
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(attempt.done)
	return attempt.receipt, attempt.err
}
