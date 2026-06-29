package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
)

// Session represents a saved conversation.
type Session struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Provider  string       `json:"provider"`
	Model     string       `json:"model"`
	Turns     []brain.Turn `json:"turns"`
	Summary   string       `json:"summary"` // first user message, for display
}

// New creates a new session with a generated ID.
func New(provider, model string) *Session {
	return &Session{
		ID:        generateID(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Provider:  provider,
		Model:     model,
	}
}

// Save persists the session to disk.
func (s *Session) Save(turns []brain.Turn) error {
	s.Turns = turns
	s.UpdatedAt = time.Now()

	// Set summary from first user message.
	if s.Summary == "" {
		for _, t := range turns {
			if t.Role == "user" {
				s.Summary = truncate(t.Content, 80)
				break
			}
		}
	}

	dir := config.SessionsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	path := filepath.Join(dir, s.ID+".json")
	return os.WriteFile(path, data, 0o644)
}

// Load reads a session from disk by ID.
func Load(id string) (*Session, error) {
	path := filepath.Join(config.SessionsDir(), id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return &s, nil
}

// Latest returns the most recently updated session, or nil if none exist.
func Latest() *Session {
	sessions := List()
	if len(sessions) == 0 {
		return nil
	}
	return &sessions[0]
}

// List returns all sessions sorted by most recently updated.
func List() []Session {
	dir := config.SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id)
		if err != nil {
			continue
		}
		sessions = append(sessions, *s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions
}

func generateID() string {
	return time.Now().Format("20060102-150405")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
