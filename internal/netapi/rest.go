package netapi

import (
	"encoding/json"
	"net/http"
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
		"turn_active":    s.dispatcher.TurnActive(),
		"providers":      s.providers,
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"fingerprint":    s.opts.Credentials.Fingerprint,
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

// handlePair exchanges a short-lived pairing code for the long-lived bearer
// token. Public (no auth) so a phone can pair without already knowing the
// token; rate-limited by the global serve limiter.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	token, err := s.opts.Credentials.ExchangePairingCode(body.Code)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       token,
		"fingerprint": s.opts.Credentials.Fingerprint,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
