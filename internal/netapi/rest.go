package netapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// SessionSummary is one row of GET /v1/sessions.
type SessionSummary struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	Turns     int       `json:"turns"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PersonaSummary is one row of GET /v1/personas.
//
// Its brain/tts values are the persona's *effective* stack — a profile's empty
// field means "inherit the app default", and a list showing blanks would tell
// a user nothing about what the agent will sound like.
type PersonaSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Active is the runtime persona, which a set_persona can move without
	// anything being persisted.
	Active bool `json:"active"`
	// Builtin is additive beyond the ADR-004 shape: a list UI needs it to lock
	// Delete on the shipped persona. Clients decode it as optional.
	Builtin bool         `json:"builtin"`
	Brain   PersonaBrain `json:"brain"`
	TTS     PersonaTTS   `json:"tts"`
}

// PersonaBrain is a persona's effective model routing.
type PersonaBrain struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PersonaTTS is a persona's effective speech settings. Tier is empty for
// providers that do not select a model tier.
type PersonaTTS struct {
	Provider string `json:"provider"`
	Voice    string `json:"voice"`
	Tier     string `json:"tier"`
}

// Providers names the configured providers for GET /v1/status. Values are
// provider names only — never secrets.
type Providers struct {
	Brain string `json:"brain"`
	STT   string `json:"stt"`
	TTS   string `json:"tts"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol_version": ProtocolVersion,
		"turn_active":      s.dispatcher.TurnActive(),
		"providers":        s.providers,
		"uptime_seconds":   int64(time.Since(s.started).Seconds()),
		"fingerprint":      s.opts.Credentials.Fingerprint,
		// Capability flags let a client gate a whole UI surface on what this
		// serve actually offers, rather than inferring it from the version.
		"meetings": s.meetings != nil,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions := []SessionSummary{}
	if s.listSessions != nil {
		sessions = s.listSessions()
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handlePersonas lists the personas this serve can switch to. Read-only: it
// mutates nothing, and the route is absent unless serve supplied the resolver.
func (s *Server) handlePersonas(w http.ResponseWriter, r *http.Request) {
	personas, err := s.opts.ListPersonas()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if personas == nil {
		personas = []PersonaSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"personas": personas})
}

// safeSessionIDPattern is the shape a session id may have before it is allowed
// anywhere near a filesystem path: no separators, no leading dot, so neither
// "../.." nor "/etc/passwd" nor a hidden name can survive it. Real ids are
// "20060102-150405-<hex>", which matches.
//
// The session store keeps its own, independent check for the same reason a
// front door and a safe both have locks: a caller that never went through HTTP
// must not be able to escape the store either. This one exists so the wire
// answers 400 ("you asked for something impossible") rather than 500 ("I
// broke"), and so the id is refused before it reaches the store at all.
var safeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// sessionIDFromPath reads {id} and refuses anything unsafe, writing the error
// response itself when it does. Checked on shape, before any filesystem call —
// the same rule resolveMeeting follows for meeting ids.
func sessionIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return "", false
	}
	if !safeSessionIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session: invalid session id"})
		return "", false
	}
	return id, true
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id, ok := sessionIDFromPath(w, r)
	if !ok {
		return
	}
	if err := s.dispatcher.ResumeSession(r.Context(), id); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"resumed": id})
}

// handleSessionDelete is only registered when Options.DeleteSession is set
// (see ListenAndServe), so it never has to nil-check the callback itself.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := sessionIDFromPath(w, r)
	if !ok {
		return
	}
	err := s.opts.DeleteSession(id)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
	case errors.Is(err, ErrSessionActive):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is active"})
	case errors.Is(err, ErrSessionNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		// An unexpected failure (permissions, disk I/O, a malformed id) is
		// not "not found" — mislabeling it as 404 would tell a client to
		// stop asking when the real problem is on this end.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// handlePair exchanges a short-lived pairing code for a long-lived bearer
// token. Public (no auth) so a phone can pair without already knowing the
// token; rate-limited by the global serve limiter.
//
// When device_name is present (PROTOCOL_DELTAS D2), a per-device token is
// minted. Without it, the primary shared token is returned (back-compat).
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body struct {
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	token, deviceID, err := s.opts.Credentials.ExchangePairingCodeForDevice(body.Code, body.DeviceName)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	out := map[string]any{
		"token":       token,
		"fingerprint": s.opts.Credentials.Fingerprint,
	}
	if deviceID != "" {
		out["device_id"] = deviceID
		out["device_name"] = strings.TrimSpace(body.DeviceName)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDevices lists paired devices (D2). Auth required.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	devices := s.opts.Credentials.ListDevices()
	if devices == nil {
		devices = []DeviceInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// handleDeviceDelete revokes one paired device and kicks its live streams.
func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE required"})
		return
	}
	id := r.PathValue("id")
	token, ok, err := s.opts.Credentials.DeleteDevice(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	s.hub.evictToken(token, "device revoked")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
