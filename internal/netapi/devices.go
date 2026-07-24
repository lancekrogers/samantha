package netapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DeviceRecord is one paired device token stored under serve/tokens/.
// The raw token is kept on disk for auth; list responses omit it.
type DeviceRecord struct {
	ID         string    `json:"id"`
	Token      string    `json:"token"`
	DeviceName string    `json:"device_name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeen   time.Time `json:"last_seen"`
}

// DeviceInfo is the public shape of GET /v1/devices (no secret token).
type DeviceInfo struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeen   time.Time `json:"last_seen"`
}

const tokensSubdir = "tokens"

// deviceStore holds per-device bearer tokens (PROTOCOL_DELTAS D2).
// The legacy single file serve/token remains the primary/shared token.
type deviceStore struct {
	mu      sync.RWMutex
	dir     string
	byID    map[string]*DeviceRecord
	byToken map[string]*DeviceRecord
}

func newDeviceStore(credDir string) *deviceStore {
	return &deviceStore{
		dir:     filepath.Join(credDir, tokensSubdir),
		byID:    make(map[string]*DeviceRecord),
		byToken: make(map[string]*DeviceRecord),
	}
}

func (s *deviceStore) load() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create device tokens dir: %w", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read device tokens dir: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID = make(map[string]*DeviceRecord)
	s.byToken = make(map[string]*DeviceRecord)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read device token %s: %w", e.Name(), err)
		}
		var rec DeviceRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("parse device token %s: %w", e.Name(), err)
		}
		if rec.ID == "" || rec.Token == "" {
			continue
		}
		cp := rec
		s.byID[rec.ID] = &cp
		s.byToken[rec.Token] = &cp
	}
	return nil
}

func (s *deviceStore) mint(deviceName string) (*DeviceRecord, error) {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return nil, fmt.Errorf("device_name is required")
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	id, err := generateDeviceID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rec := &DeviceRecord{
		ID:         id,
		Token:      token,
		DeviceName: deviceName,
		CreatedAt:  now,
		LastSeen:   now,
	}
	if err := s.write(rec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.byID[rec.ID] = rec
	s.byToken[rec.Token] = rec
	s.mu.Unlock()
	return rec, nil
}

func (s *deviceStore) write(rec *DeviceRecord) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create device tokens dir: %w", err)
	}
	path := filepath.Join(s.dir, rec.ID+".json")
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device token: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write device token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit device token: %w", err)
	}
	return nil
}

func (s *deviceStore) list() []DeviceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceInfo, 0, len(s.byID))
	for _, rec := range s.byID {
		out = append(out, DeviceInfo{
			ID:         rec.ID,
			DeviceName: rec.DeviceName,
			CreatedAt:  rec.CreatedAt,
			LastSeen:   rec.LastSeen,
		})
	}
	return out
}

// delete removes one device by id. Returns the revoked token so callers can
// evict live streams for that bearer only.
func (s *deviceStore) delete(id string) (token string, ok bool, err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false, fmt.Errorf("missing device id")
	}
	s.mu.Lock()
	rec, exists := s.byID[id]
	if !exists {
		s.mu.Unlock()
		return "", false, nil
	}
	token = rec.Token
	delete(s.byID, id)
	delete(s.byToken, rec.Token)
	s.mu.Unlock()

	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return token, true, fmt.Errorf("remove device token file: %w", err)
	}
	return token, true, nil
}

func (s *deviceStore) acceptToken(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byToken[token]
	return ok
}

// lastSeenMinInterval avoids rewriting token files on every request.
const lastSeenMinInterval = 30 * time.Second

func (s *deviceStore) touch(token string) {
	s.mu.Lock()
	rec, ok := s.byToken[token]
	if !ok {
		s.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	if now.Sub(rec.LastSeen) < lastSeenMinInterval {
		s.mu.Unlock()
		return
	}
	rec.LastSeen = now
	// Copy for write outside lock after unlocking map.
	cp := *rec
	s.mu.Unlock()
	// Best-effort persistence; auth path must not fail on disk hiccups.
	_ = s.write(&cp)
}

// clearAll deletes every device token file and empties the in-memory maps.
// Used by --revoke-tokens (all-or-nothing).
func (s *deviceStore) clearAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byID {
		path := filepath.Join(s.dir, id+".json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove device token %s: %w", id, err)
		}
	}
	s.byID = make(map[string]*DeviceRecord)
	s.byToken = make(map[string]*DeviceRecord)
	return nil
}

func generateDeviceID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	// Prefer a stable-looking id that is not the raw token.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// constantTimeTokenMatch compares presented against want without leaking length
// via early exit on unequal lengths (pad both to equal for subtle).
func constantTimeTokenMatch(presented, want string) bool {
	if subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1 {
		return true
	}
	return false
}
