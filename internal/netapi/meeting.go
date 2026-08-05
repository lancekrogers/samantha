package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

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

// writeMeetingError maps the meeting package's sentinel errors onto the status
// codes the protocol promises.
func writeMeetingError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, remote.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, remote.ErrMeetingActive), errors.Is(err, remote.ErrProcessing):
		status = http.StatusConflict
	case errors.Is(err, remote.ErrNotRecording):
		status = http.StatusConflict
	case errors.Is(err, remote.ErrBadSegment), errors.Is(err, remote.ErrBadControl):
		status = http.StatusBadRequest
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
