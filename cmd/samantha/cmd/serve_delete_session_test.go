//go:build !integration

package cmd

import (
	"errors"
	"testing"

	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

func TestSessionRefID(t *testing.T) {
	ref := &sessionRef{sess: session.New("ollama", "qwen3:8b")}
	first := ref.sess.ID
	if got := ref.id(); got != first {
		t.Fatalf("id() = %q, want %q", got, first)
	}

	swapped := session.New("claude", "sonnet")
	ref.swap(swapped)
	if got := ref.id(); got != swapped.ID {
		t.Fatalf("id() after swap = %q, want %q", got, swapped.ID)
	}
	if swapped.ID == first {
		t.Fatal("test fixture bug: the two generated sessions share an id")
	}
}

// The live-session guard: deleting the session this process is currently
// writing into must refuse rather than delete, since the pipeline would just
// rewrite the file on the next turn.
func TestServeDeleteSessionRefusesLiveSession(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := &sessionRef{sess: session.New("ollama", "qwen3:8b")}
	del := serveDeleteSession(ref)

	err := del(ref.sess.ID)
	if !errors.Is(err, netapi.ErrSessionActive) {
		t.Fatalf("serveDeleteSession()(live id) = %v, want netapi.ErrSessionActive", err)
	}
}

func TestServeDeleteSessionDeletesNonLiveSession(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := &sessionRef{sess: session.New("ollama", "qwen3:8b")}
	del := serveDeleteSession(ref)

	other := session.New("claude", "sonnet")
	if err := other.Save(nil); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := del(other.ID); err != nil {
		t.Fatalf("serveDeleteSession()(other id) error = %v", err)
	}
	if _, err := session.Load(other.ID); err == nil {
		t.Fatal("session file still loadable after delete")
	}
}

func TestServeDeleteSessionUnknownIDReturnsSentinel(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := &sessionRef{sess: session.New("ollama", "qwen3:8b")}
	del := serveDeleteSession(ref)

	// The DELETE route's handler distinguishes "not found" (404) from an
	// unexpected store failure (500) via netapi's own sentinel — it must
	// never have to import the session package to do that — so
	// serveDeleteSession translates session.ErrSessionNotFound into
	// netapi.ErrSessionNotFound rather than passing the session package's
	// sentinel straight through.
	err := del("20260101-000000-dead")
	if !errors.Is(err, netapi.ErrSessionNotFound) {
		t.Fatalf("serveDeleteSession()(unknown id) = %v, want netapi.ErrSessionNotFound", err)
	}
	if errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("serveDeleteSession()(unknown id) = %v, must not be session.ErrSessionNotFound directly (netapi must not depend on the session package's error types)", err)
	}
}

// A resumed session becomes the live one: the guard must follow swap(), not
// just the id ref started with.
func TestServeDeleteSessionFollowsResume(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := &sessionRef{sess: session.New("ollama", "qwen3:8b")}
	originalID := ref.sess.ID

	resumed := session.New("claude", "sonnet")
	if err := resumed.Save(nil); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ref.swap(resumed)

	del := serveDeleteSession(ref)
	if err := del(resumed.ID); !errors.Is(err, netapi.ErrSessionActive) {
		t.Fatalf("delete(resumed id) = %v, want ErrSessionActive after swap", err)
	}
	// The original (no longer live) id is now an ordinary session: it was
	// never saved to disk in this test, so deleting it reports not-found
	// rather than a false "active" guard.
	if err := del(originalID); !errors.Is(err, netapi.ErrSessionNotFound) {
		t.Fatalf("delete(original id) = %v, want netapi.ErrSessionNotFound (no longer the live session)", err)
	}
}
