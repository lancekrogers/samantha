package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// ErrSessionNotFound is returned by Store.Delete when no session with the
// given id exists, so both the CLI and the serve handler can map it (e.g. to
// a 404) without string-matching an error message.
var ErrSessionNotFound = errors.New("session: not found")

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
	return DefaultStore().Save(s, turns)
}

// SaveBackup writes the given turns to a sibling <id>.pre-compact.json so one
// recovery generation survives /compact rewriting the live session. Each
// compact overwrites the previous backup; the main session file is untouched.
func (s *Session) SaveBackup(turns []brain.Turn) error {
	return DefaultStore().SaveBackup(s, turns)
}

func (s *Session) saveBackupTo(dir string, turns []brain.Turn) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	// Copy so the backup never mutates the live session's Turns/UpdatedAt.
	backup := *s
	backup.Turns = normalizeTurns(turns)
	backup.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(&backup, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session backup: %w", err)
	}
	tmp, err := os.CreateTemp(dir, s.ID+".bak-tmp-*")
	if err != nil {
		return fmt.Errorf("create backup temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close backup temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, s.ID+".pre-compact.json")); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save session backup: %w", err)
	}
	return nil
}

func (s *Session) saveTo(dir string, turns []brain.Turn) error {
	s.Turns = normalizeTurns(turns)
	s.UpdatedAt = time.Now()

	// Set summary from first user message.
	if s.Summary == "" {
		for _, t := range s.Turns {
			if t.Role == "user" {
				s.Summary = truncate(t.Content, 80)
				break
			}
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	// Write to a temp file and rename so a crash mid-write can't corrupt an
	// existing session.
	tmp, err := os.CreateTemp(dir, s.ID+".tmp-*")
	if err != nil {
		return fmt.Errorf("create session temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close session temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, s.ID+".json")); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// normalizeTurns maps provider-specific roles to the persisted scheme:
// "samantha" (claude/grok replies) is stored as "assistant".
func normalizeTurns(turns []brain.Turn) []brain.Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]brain.Turn, len(turns))
	for i, t := range turns {
		if t.Role == "samantha" {
			t.Role = "assistant"
		}
		out[i] = t
	}
	return out
}

// Load reads a session from disk by ID.
func Load(id string) (*Session, error) {
	return DefaultStore().Load(id)
}

func loadFrom(dir, id string) (*Session, error) {
	path := filepath.Join(dir, id+".json")
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
	return DefaultStore().List()
}

func listIn(dir string) []Session {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Pre-compact backups sit beside their session; they are recovery
		// artifacts, not sessions.
		if strings.HasSuffix(e.Name(), ".pre-compact.json") {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := loadFrom(dir, id)
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
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:])
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// Store persists sessions under an explicit directory.
//
// The package-level Save/Load/List/Latest resolve their directory from global
// config, which is correct for the CLI and wrong for a library: an embedder
// holding its own configuration must not have its sessions written to whatever
// path the process last loaded. Store is the injectable form; the package-level
// functions delegate to it so both paths share one implementation.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// DefaultStore returns a Store rooted at the process-wide sessions directory.
// This is what the CLI uses, and what the package-level helpers delegate to.
func DefaultStore() *Store { return NewStore(config.SessionsDir()) }

// Save writes the session's turns.
func (st *Store) Save(s *Session, turns []brain.Turn) error { return s.saveTo(st.dir, turns) }

// SaveBackup writes a backup copy, used before a destructive operation.
func (st *Store) SaveBackup(s *Session, turns []brain.Turn) error {
	return s.saveBackupTo(st.dir, turns)
}

// Load reads a session by id.
func (st *Store) Load(id string) (*Session, error) { return loadFrom(st.dir, id) }

// List returns every session in the store, newest first.
func (st *Store) List() []Session { return listIn(st.dir) }

// Latest returns the most recent session, or nil when the store is empty.
func (st *Store) Latest() *Session {
	sessions := st.List()
	if len(sessions) == 0 {
		return nil
	}
	newest := sessions[0]
	return &newest
}

// Delete removes the session file <id>.json and its pre-compact backup
// (<id>.pre-compact.json, best-effort — its absence is not an error).
// Returns ErrSessionNotFound when no session with that id exists. id is
// validated before any filesystem call: a path separator or a ".." segment
// is rejected outright, so a delete verb can never escape the store's
// directory no matter what a caller passes.
func (st *Store) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}

	path := filepath.Join(st.dir, id+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("delete session %s: %w", id, err)
	}

	backup := filepath.Join(st.dir, id+".pre-compact.json")
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session %s backup: %w", id, err)
	}
	return nil
}

// validateSessionID rejects an id that could escape the store's directory
// once joined into a path: empty, containing a path separator (either OS's,
// regardless of the host), or containing ".." anywhere. Checked before any
// filesystem call, not after.
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session: empty id")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("session: invalid id %q", id)
	}
	return nil
}
