package speaker

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// NameMap maps stable live labels (speaker-1) to human display names for the
// current conversation. Renames are session-local: they affect UI bubbles and
// model prompt attribution, not the embedding manager's provisional ids.
type NameMap struct {
	mu    sync.RWMutex
	names map[string]string // normalized id → display name
}

// NewNameMap returns an empty rename table.
func NewNameMap() *NameMap {
	return &NameMap{names: make(map[string]string)}
}

// NormalizeID accepts "1", "speaker-1", or "Speaker-1" and returns "speaker-N".
// Empty or invalid input returns "".
func NormalizeID(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, LabelSpeakerPrefix) {
		rest := strings.TrimPrefix(s, LabelSpeakerPrefix)
		if rest == "" {
			return ""
		}
		for _, r := range rest {
			if r < '0' || r > '9' {
				return ""
			}
		}
		if rest[0] == '0' {
			return ""
		}
		return LabelSpeakerPrefix + rest
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if s[0] == '0' {
		return ""
	}
	return LabelSpeakerPrefix + s
}

// Set binds a human name to a stable speaker id. Empty name clears the binding.
func (m *NameMap) Set(id, name string) error {
	if m == nil {
		return fmt.Errorf("speaker: name map is nil")
	}
	id = NormalizeID(id)
	if id == "" {
		return fmt.Errorf("speaker: invalid speaker id")
	}
	name = strings.TrimSpace(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		delete(m.names, id)
		return nil
	}
	m.names[id] = name
	return nil
}

// Get returns the bound display name, or "" when unset.
func (m *NameMap) Get(id string) string {
	if m == nil {
		return ""
	}
	id = NormalizeID(id)
	if id == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.names[id]
}

// Display returns the human name when set, otherwise the stable id (or "").
// Implements brain.SpeakerNames.
func (m *NameMap) Display(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if name := m.Get(id); name != "" {
		return name
	}
	if n := NormalizeID(id); n != "" {
		return n
	}
	return id
}

// Snapshot returns a sorted copy of id→name bindings for status display.
func (m *NameMap) Snapshot() []NameBinding {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]NameBinding, 0, len(m.names))
	for id, name := range m.names {
		out = append(out, NameBinding{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NameBinding is one rename entry for status listing.
type NameBinding struct {
	ID   string
	Name string
}
