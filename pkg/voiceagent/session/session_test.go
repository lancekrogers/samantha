package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		turns []brain.Turn
		want  []brain.Turn
	}{
		{
			name:  "empty turns",
			turns: nil,
			want:  nil,
		},
		{
			name: "samantha role persisted as assistant",
			turns: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "samantha", Content: "hello"},
			},
			want: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
		},
		{
			name: "ollama roles pass through",
			turns: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "calling a tool"},
				{Role: "tool", Content: "result"},
				{Role: "assistant", Content: "done"},
			},
			want: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "calling a tool"},
				{Role: "tool", Content: "result"},
				{Role: "assistant", Content: "done"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := New("claude", "claude")
			if err := s.saveTo(dir, tt.turns); err != nil {
				t.Fatalf("saveTo() error = %v", err)
			}

			got, err := loadFrom(dir, s.ID)
			if err != nil {
				t.Fatalf("loadFrom() error = %v", err)
			}
			if !reflect.DeepEqual(got.Turns, tt.want) {
				t.Errorf("Turns = %+v, want %+v", got.Turns, tt.want)
			}
			if got.ID != s.ID || got.Provider != "claude" || got.Model != "claude" {
				t.Errorf("metadata = (%s, %s, %s), want (%s, claude, claude)", got.ID, got.Provider, got.Model, s.ID)
			}
		})
	}
}

func TestLoadMissingSession(t *testing.T) {
	if _, err := loadFrom(t.TempDir(), "nope"); err == nil {
		t.Fatal("loadFrom() = nil error for missing session")
	}
}

func TestGenerateIDUniqueAcrossRapidCalls(t *testing.T) {
	format := regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)
	seen := make(map[string]bool)
	for range 4 {
		id := generateID()
		if !format.MatchString(id) {
			t.Fatalf("generateID() = %q, want timestamp with 4-hex suffix", id)
		}
		if seen[id] {
			t.Fatalf("generateID() produced duplicate %q", id)
		}
		seen[id] = true
	}
}

func TestListOrderingAndCorruptSkip(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var ids []string
	for i := range 3 {
		s := New("ollama", "m")
		turns := []brain.Turn{{Role: "user", Content: fmt.Sprintf("msg %d", i)}}
		if err := s.saveTo(dir, turns); err != nil {
			t.Fatalf("saveTo() error = %v", err)
		}
		ids = append(ids, s.ID)
		time.Sleep(2 * time.Millisecond) // distinct UpdatedAt
	}

	sessions := listIn(dir)
	if len(sessions) != 3 {
		t.Fatalf("len(listIn()) = %d, want 3 (corrupt/non-json files skipped)", len(sessions))
	}
	for i, want := range []string{ids[2], ids[1], ids[0]} {
		if sessions[i].ID != want {
			t.Errorf("sessions[%d].ID = %s, want %s (most recent first)", i, sessions[i].ID, want)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s after save", e.Name())
		}
	}
}

func TestSaveOverwritesExistingSessionAtomically(t *testing.T) {
	dir := t.TempDir()
	s := New("ollama", "m")

	if err := s.saveTo(dir, []brain.Turn{{Role: "user", Content: "first"}}); err != nil {
		t.Fatalf("saveTo() error = %v", err)
	}
	if err := s.saveTo(dir, []brain.Turn{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
	}); err != nil {
		t.Fatalf("saveTo() error = %v", err)
	}

	got, err := loadFrom(dir, s.ID)
	if err != nil {
		t.Fatalf("loadFrom() error = %v", err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("len(Turns) = %d, want 2", len(got.Turns))
	}
	if got.Summary != "first" {
		t.Errorf("Summary = %q, want %q", got.Summary, "first")
	}
}

func TestSaveBackupWritesSiblingWithoutMutatingSession(t *testing.T) {
	dir := t.TempDir()
	s := New("claude", "sonnet")
	live := []brain.Turn{{Role: "user", Content: "live"}}
	if err := s.saveTo(dir, live); err != nil {
		t.Fatalf("saveTo() error: %v", err)
	}

	preCompact := []brain.Turn{
		{Role: "user", Content: "old one"},
		{Role: "samantha", Content: "old reply"},
	}
	if err := s.saveBackupTo(dir, preCompact); err != nil {
		t.Fatalf("saveBackupTo() error: %v", err)
	}

	// The live session's in-memory turns are untouched by the backup.
	if len(s.Turns) != 1 || s.Turns[0].Content != "live" {
		t.Fatalf("session turns mutated by backup: %+v", s.Turns)
	}

	// The backup file exists, holds the pre-compact turns (normalized), and
	// does not appear in the session listing.
	data, err := os.ReadFile(filepath.Join(dir, s.ID+".pre-compact.json"))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	var backup Session
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	if len(backup.Turns) != 2 || backup.Turns[1].Role != "assistant" {
		t.Fatalf("backup turns = %+v, want 2 normalized turns", backup.Turns)
	}

	sessions := listIn(dir)
	if len(sessions) != 1 || sessions[0].ID != s.ID {
		t.Fatalf("listIn = %d sessions, want only the live session", len(sessions))
	}
}
