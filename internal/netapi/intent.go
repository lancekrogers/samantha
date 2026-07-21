package netapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IntentSinkConfig routes POST /v1/intent (PROTOCOL_DELTAS D3).
// Zero value uses file mode under Dir/intents.
type IntentSinkConfig struct {
	// Mode: "file" (default), "camp", or "webhook" (webhook deferred).
	Mode string
	// Dir is the file-mode destination root (default: credentials Dir/intents).
	Dir string
	// CampBin and CampaignRoot for mode=camp.
	CampBin      string
	CampaignRoot string
}

// IntentRequest is the JSON body of POST /v1/intent.
type IntentRequest struct {
	Type       string `json:"type"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body"`
	Concept    string `json:"concept,omitempty"`
	Campaign   string `json:"campaign,omitempty"`
	Source     string `json:"source"`
	CapturedAt string `json:"captured_at"`
}

func (s *Server) handleIntent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var req IntentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is required"})
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	if req.Type == "" {
		req.Type = "note"
	}
	if req.Title == "" {
		req.Title = deriveIntentTitle(req.Body)
	}
	if req.CapturedAt == "" {
		req.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if req.Source == "" {
		req.Source = "unknown"
	}

	id, path, err := s.writeIntentFile(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "path": path})
}

func (s *Server) handleIntentTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	// Static default until camp-mode target discovery is configured.
	targets := []map[string]string{
		{"campaign": "default", "concept": ""},
	}
	if s.opts.IntentSink.CampaignRoot != "" {
		targets = []map[string]string{
			{"campaign": filepath.Base(s.opts.IntentSink.CampaignRoot)},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

func (s *Server) writeIntentFile(req IntentRequest) (id, path string, err error) {
	dir := s.opts.IntentSink.Dir
	if dir == "" && s.opts.Credentials != nil && s.opts.Credentials.Dir != "" {
		dir = filepath.Join(s.opts.Credentials.Dir, "intents")
	}
	if dir == "" {
		return "", "", fmt.Errorf("intent sink directory not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create intent dir: %w", err)
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	} else {
		id = hex.EncodeToString(raw)
	}
	path = filepath.Join(dir, id+".json")
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("write intent: %w", err)
	}
	return id, path, nil
}

func deriveIntentTitle(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "untitled"
	}
	// First line / first 72 runes.
	line := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		line = body[:i]
	}
	runes := []rune(line)
	if len(runes) > 72 {
		return string(runes[:72]) + "…"
	}
	return line
}
