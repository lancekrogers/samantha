package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
)

// Meeting endpoints (PROTOCOL_DELTAS D6) let a phone record a meeting through
// serve without touching the WebSocket, the dispatcher, or the remote
// microphone claim: audio arrives as sequenced, individually acknowledged
// segment uploads and is transcribed after the recording stops.

func (s *Server) handleMeetingStart(w http.ResponseWriter, r *http.Request) {
	manager := s.meetings
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "meeting capture is not configured"})
		return
	}
	var req remote.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	session, err := manager.Start(r.Context(), req)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, remote.StartResponse{
		MeetingID:         session.ID(),
		SegmentSeconds:    manager.SegmentSeconds(),
		OutboxCapSegments: manager.OutboxCapSegments(),
	})
}

// handleMeetingSegment takes one raw pcm_s16le body. 204 is the ack the
// client's outbox waits for; a duplicate upload gets the same 204 so retries
// after a dropped response are always safe.
func (s *Server) handleMeetingSegment(w http.ResponseWriter, r *http.Request) {
	session, manager, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sequence must be a non-negative integer"})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !isBinaryContentType(ct) {
		writeJSON(w, http.StatusUnsupportedMediaType,
			map[string]string{"error": "segment body must be raw pcm_s16le, not " + ct})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remote.MaxSegmentBytes+1))
	if err != nil {
		// A dropped connection is not a size problem; say which one it was.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "segment body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read segment body"})
		return
	}
	if err := session.AppendSegment(r.Context(), seq, body, manager.Now()); err != nil {
		writeMeetingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMeetingControl(w http.ResponseWriter, r *http.Request) {
	session, manager, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	var req remote.ControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	if err := session.Control(r.Context(), req, manager.Now()); err != nil {
		writeMeetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session.Status())
}

// handleMeetingStop finalizes the recording. A non-empty missing_seqs in the
// response means the server is still short of audio: the client re-pushes
// those sequences and stops again.
func (s *Server) handleMeetingStop(w http.ResponseWriter, r *http.Request) {
	session, manager, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	var req remote.StopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	status, err := session.Stop(r.Context(), req.LastSeq, manager.Now())
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleMeetingStatus(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, session.Status())
}

// handleMeetingRoute files a finished meeting into a campaign's
// notes/meetings via the CI0009 importer. Retrying the same campaign answers
// from the session's receipt cache: the importer does not dedupe, and a
// timed-out response must not turn into a second filed note.
func (s *Server) handleMeetingRoute(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	if s.routeMeeting == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "meeting routing is not configured"})
		return
	}
	var req remote.RouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	campaign := strings.TrimSpace(req.Campaign)
	if campaign == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaign is required"})
		return
	}
	summary, err := session.Summary()
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	// Key by normalized capture + campaign: retries of the same route share
	// one execution and one receipt, while a different capture mode for the
	// same campaign is a genuinely new route.
	key := meeting.NormalizeCampaignCapture(req.Capture) + "\x00" + campaign
	receipt, err := session.RouteOnce(key, func() (remote.RouteReceipt, error) {
		return s.routeMeeting(r.Context(), summary, campaign, req.Capture)
	})
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// meetingSession resolves the path's meeting id, writing the error response
// itself when it cannot.
func (s *Server) meetingSession(w http.ResponseWriter, r *http.Request) (*remote.Session, *remote.Manager, bool) {
	manager := s.meetings
	if manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "meeting capture is not configured"})
		return nil, nil, false
	}
	session, err := manager.Session(r.PathValue("id"))
	if err != nil {
		writeMeetingError(w, err)
		return nil, nil, false
	}
	return session, manager, true
}

// isBinaryContentType accepts the octet-stream family a raw PCM upload should
// declare. A client posting JSON by mistake would otherwise have it stored as
// audio and played back as noise.
func isBinaryContentType(contentType string) bool {
	if media, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = media
	}
	switch contentType {
	case "application/octet-stream", "audio/l16", "audio/pcm", "audio/x-pcm":
		return true
	}
	return false
}

// writeMeetingError maps the meeting package's sentinel errors onto the status
// codes the protocol promises.
//
// Only mapped (4xx) errors carry their text to the client: an unmapped failure
// is an internal one, and its message tends to contain absolute bundle paths
// and meeting titles that have no business crossing the wire.
func writeMeetingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeMeetingProblem(w, http.StatusNotFound, err)
	case errors.Is(err, remote.ErrMeetingActive), errors.Is(err, remote.ErrProcessing),
		errors.Is(err, remote.ErrNotRecording), errors.Is(err, remote.ErrNotRoutable):
		writeMeetingProblem(w, http.StatusConflict, err)
	case errors.Is(err, remote.ErrBadSegment), errors.Is(err, remote.ErrBadControl),
		errors.Is(err, remote.ErrBadStart), errors.Is(err, remote.ErrNoteText):
		writeMeetingProblem(w, http.StatusBadRequest, err)
	case errors.Is(err, meeting.ErrImportMeetingUnsupported):
		// 412: the phone's request was fine; the Mac's camp predates CI0009.
		// The text carries the remediation the UI shows verbatim.
		writeMeetingProblem(w, http.StatusPreconditionFailed, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeMeetingProblem(w, http.StatusServiceUnavailable, err)
	default:
		log.Printf("netapi: meeting request failed: %v", err)
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "meeting request failed"})
	}
}

func writeMeetingProblem(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// handleMeetingDocument returns the finished meeting's canonical meeting.md.
// The phone renders this for speaker-labeled results instead of a parallel
// wire projection: the document the Mac wrote — sections, speaker labels,
// notes — is the format, and inventing a second one would drift from it.
func (s *Server) handleMeetingDocument(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.meetingSession(w, r)
	if !ok {
		return
	}
	if _, err := session.Summary(); err != nil {
		writeMeetingError(w, err)
		return
	}
	document, err := os.ReadFile(filepath.Join(session.BundlePath(), meetinglog.BundleDocumentName))
	if err != nil {
		log.Printf("netapi: meeting document read failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "meeting document unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}
