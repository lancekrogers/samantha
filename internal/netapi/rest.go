package netapi

import (
	"encoding/json"
	"net/http"
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
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions := []SessionSummary{}
	if s.listSessions != nil {
		sessions = s.listSessions()
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session id"})
		return
	}
	if err := s.dispatcher.ResumeSession(r.Context(), id); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"resumed": id})
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
