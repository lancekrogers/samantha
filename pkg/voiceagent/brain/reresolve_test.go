package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
)

// writePersonaDoc writes a kind=persona document named `name` under dir.
func writePersonaDoc(t *testing.T, dir, name, identity string) {
	t.Helper()
	sub := filepath.Join(dir, string(prompts.KindPersona))
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "schema: samantha.prompt.v1\nprompt:\n  name: " + name +
		"\n  kind: persona\n  system_prompt: |\n    " + identity +
		"\nmetadata:\n  id: " + name + "-test\n  version: 1\n  description: test\n"
	if err := os.WriteFile(filepath.Join(sub, name+".yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testCfg(dir string) *config.Config {
	return &config.Config{PromptsDir: dir, AgentName: "Samantha", Persona: "alpha"}
}

// Error case first: a document that fails to parse mid-session must not end the
// turn. Bricking a live conversation because a file was caught mid-save is
// worse than speaking with a slightly stale personality.
func TestReloaderKeepsLastGoodOnBrokenDocument(t *testing.T) {
	dir := t.TempDir()
	writePersonaDoc(t, dir, "alpha", "You are Alpha.")
	cfg := testCfg(dir)

	r := newPromptReloader(prompts.KindPersona, "alpha", "You are Alpha.", nil)

	// Corrupt the document the way a half-finished save would.
	if err := os.WriteFile(filepath.Join(dir, "persona", "alpha.yaml"), []byte("schema: samantha.prompt.v1\nprompt:\n  name: alpha\n  kind: per"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, changed, err := r.resolve(cfg)
	if err == nil {
		t.Error("expected a resolution error for a broken document")
	}
	if changed {
		t.Error("a failed resolution must not report a change")
	}
	if !strings.Contains(text, "You are Alpha.") {
		t.Errorf("expected the last good prompt to be kept, got %q", text)
	}
}

// First half of the R-P2 acceptance: an edit to the bound document reaches the
// next turn without a restart.
func TestReloaderPicksUpEditsToTheBoundDocument(t *testing.T) {
	dir := t.TempDir()
	writePersonaDoc(t, dir, "alpha", "You are Alpha.")
	cfg := testCfg(dir)

	r := newPromptReloader(prompts.KindPersona, "alpha", "You are Alpha.", nil)

	if _, changed, err := r.resolve(cfg); err != nil || changed {
		t.Fatalf("first resolve of unchanged content: changed=%v err=%v", changed, err)
	}

	writePersonaDoc(t, dir, "alpha", "You are Alpha, rewritten.")

	text, changed, err := r.resolve(cfg)
	if err != nil {
		t.Fatalf("resolve after edit: %v", err)
	}
	if !changed {
		t.Error("an edited document must report changed")
	}
	if !strings.Contains(text, "rewritten") {
		t.Errorf("expected the edited text, got %q", text)
	}
}

// Second half of the R-P2 acceptance, and the more important one: this protects
// a deliberate guarantee rather than fixing a bug.
//
// A session binds to a persona at conversation start and must keep that identity
// for its whole life — see internal/persona/binding.go. So the reloader is bound
// to a document *name* captured once; switching the live config to another
// persona must not retarget a conversation already in flight.
//
// If this test is ever deleted as redundant, read binding.go first.
func TestReloaderIgnoresLiveConfigPersonaSwitch(t *testing.T) {
	dir := t.TempDir()
	writePersonaDoc(t, dir, "alpha", "You are Alpha.")
	writePersonaDoc(t, dir, "beta", "You are Beta.")

	cfg := testCfg(dir)
	r := newPromptReloader(prompts.KindPersona, "alpha", "You are Alpha.", nil)

	// Someone runs `persona use beta` mid-conversation: the live config now
	// points elsewhere.
	cfg.Persona = "beta"

	text, changed, err := r.resolve(cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if changed {
		t.Error("a persona switch must not change an in-flight session")
	}
	if strings.Contains(text, "Beta") {
		t.Errorf("session bound to alpha resolved beta's document: %q", text)
	}
	if !strings.Contains(text, "Alpha") {
		t.Errorf("expected alpha's document, got %q", text)
	}
}

// The hash is what makes "is the model seeing my edit?" answerable, so it must
// track content rather than identity.
func TestReloaderHashTracksContent(t *testing.T) {
	dir := t.TempDir()
	writePersonaDoc(t, dir, "alpha", "You are Alpha.")
	cfg := testCfg(dir)

	r := newPromptReloader(prompts.KindPersona, "alpha", "You are Alpha.", nil)
	before := r.hash()

	writePersonaDoc(t, dir, "alpha", "You are Alpha, rewritten.")
	if _, _, err := r.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if r.hash() == before {
		t.Error("hash must change when the document content changes")
	}
}

// A changed-content callback fires only on an actual change: ollama rebuilds its
// assembled system prompt from it, and rebuilding every turn would churn the
// server-side prefix cache for nothing.
func TestReloaderOnChangeFiresOnlyOnRealChanges(t *testing.T) {
	dir := t.TempDir()
	writePersonaDoc(t, dir, "alpha", "You are Alpha.")
	cfg := testCfg(dir)

	var calls int
	r := newPromptReloader(prompts.KindPersona, "alpha", "You are Alpha.", func(string) { calls++ })

	for range 3 {
		if _, _, err := r.resolve(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Errorf("unchanged content fired onChange %d times", calls)
	}

	writePersonaDoc(t, dir, "alpha", "You are Alpha, rewritten.")
	if _, _, err := r.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected exactly one onChange after an edit, got %d", calls)
	}
}
