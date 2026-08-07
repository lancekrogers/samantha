package netapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
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
	// Context links a mid-meeting quick capture to its moment (plan §2.3).
	// Optional and strictly additive: absent for plain captures, and the file
	// sink emits it only when set, so existing consumers see identical JSON.
	Context *IntentContext `json:"context,omitempty"`
}

// IntentContext is the meeting moment an idea was captured at. The offset is
// meeting-relative audio time — the same clock as bundle bookmarks — so the
// intent can point back into the transcript.
type IntentContext struct {
	MeetingID string `json:"meeting_id"`
	OffsetMs  int64  `json:"offset_ms"`
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
	if req.Context != nil {
		req.Context.MeetingID = strings.TrimSpace(req.Context.MeetingID)
		if req.Context.MeetingID == "" || req.Context.OffsetMs < 0 {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "context requires meeting_id and a non-negative offset_ms"})
			return
		}
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
	// Live registry via `camp list --json`, so every phone picker — intent
	// capture and the meeting destination — offers the same campaigns the Mac
	// actually routes to. Short timeout: this populates a UI, and the static
	// fallback below keeps an answer coming when camp is slow or absent.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if camps, err := meeting.ListCampaigns(ctx, meeting.DefaultRunner, meeting.DefaultLookPath); err == nil && len(camps) > 0 {
		targets := make([]map[string]string, 0, len(camps))
		for _, c := range camps {
			targets = append(targets, map[string]string{"campaign": c.Name})
		}
		writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
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
	return WriteIntentFile(dir, req)
}

// WriteIntentFile persists one intent into the file sink `POST /v1/intent`
// uses, under a fresh random id — each call files a new intent.
func WriteIntentFile(dir string, req IntentRequest) (id, path string, err error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	} else {
		id = hex.EncodeToString(raw)
	}
	path, _, err = WriteIntentFileWithID(dir, id, req)
	return id, path, err
}

// WriteIntentFileWithID persists one intent under a caller-chosen id with
// atomic create-if-absent semantics. The complete payload is written and
// synced in the destination directory before a hard link publishes it under
// the final name, so a crash cannot leave a partial receipt at path. An
// existing valid receipt reports created=false; an invalid legacy/partial
// receipt is removed and recovered from the staged payload.
func WriteIntentFileWithID(dir, id string, req IntentRequest) (path string, created bool, err error) {
	if dir == "" {
		return "", false, fmt.Errorf("intent sink directory not configured")
	}
	if id == "" {
		return "", false, fmt.Errorf("intent id is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create intent dir: %w", err)
	}
	path = filepath.Join(dir, id+".json")
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", false, err
	}
	payload = append(payload, '\n')

	staged, err := os.CreateTemp(dir, "."+id+"-*.tmp")
	if err != nil {
		return "", false, fmt.Errorf("stage intent: %w", err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()

	if _, err := staged.Write(payload); err != nil {
		_ = staged.Close()
		return "", false, fmt.Errorf("write staged intent: %w", err)
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return "", false, fmt.Errorf("sync staged intent: %w", err)
	}
	if err := staged.Close(); err != nil {
		return "", false, fmt.Errorf("close staged intent: %w", err)
	}

	// Link is an atomic no-replace publish because stagedPath and path live in
	// the same directory. At most one concurrent writer can create path.
	for recoveryAttempt := 0; recoveryAttempt < 2; recoveryAttempt++ {
		if err := os.Link(stagedPath, path); err == nil {
			return path, true, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", false, fmt.Errorf("publish intent: %w", err)
		}

		valid, err := validIntentReceipt(path)
		if err != nil {
			return "", false, err
		}
		if valid {
			return path, false, nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("remove invalid intent receipt: %w", err)
		}
	}
	return "", false, fmt.Errorf("publish intent: could not replace invalid receipt %s", path)
}

func validIntentReceipt(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read existing intent receipt: %w", err)
	}
	var existing IntentRequest
	if err := json.Unmarshal(raw, &existing); err != nil {
		return false, nil
	}
	return strings.TrimSpace(existing.Body) != "", nil
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
