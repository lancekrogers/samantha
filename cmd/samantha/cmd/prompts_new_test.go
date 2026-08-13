//go:build !integration

package cmd

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
)

// Error cases first.

// Personas must route to `persona create`: two ways to make a persona is how the
// two paths drift, and the profile would be missing either way.
func TestPromptsNewRefusesPersonaWithAPointer(t *testing.T) {
	out, err := runRootForPrompts(t, t.TempDir(), "prompts", "new", "persona", "pirate")
	if err == nil {
		t.Fatal("expected persona to be refused")
	}
	if !strings.Contains(err.Error()+out, "persona create") {
		t.Errorf("refusal should point at `persona create`, got: %v %s", err, out)
	}
}

func TestPromptsNewRejectsUnknownKind(t *testing.T) {
	_, err := runRootForPrompts(t, t.TempDir(), "prompts", "new", "bogus", "x")
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("expected an unknown-kind error, got %v", err)
	}
}

// Scaffolding must never clobber an existing document — losing a hand-edited
// prompt to a mistyped command is unrecoverable.
func TestPromptsNewNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := runRootForPrompts(t, dir, "prompts", "new", "turn", "brisk"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := runRootForPrompts(t, dir, "prompts", "new", "turn", "brisk")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected an already-exists error, got %v", err)
	}
}

// The scaffolded file must be a valid, resolvable document — a scaffold that
// produces something the resolver rejects is worse than no scaffold.
func TestPromptsNewProducesAResolvableDocument(t *testing.T) {
	dir := t.TempDir()
	for _, kind := range []prompts.Kind{prompts.KindTurn, prompts.KindCompact} {
		name := "scaffolded-" + string(kind)
		if _, err := runRootForPrompts(t, dir, "prompts", "new", string(kind), name); err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		doc, err := (prompts.Resolver{UserDir: dir}).Resolve(kind, name)
		if err != nil {
			t.Fatalf("scaffolded %s document does not resolve: %v", kind, err)
		}
		if strings.TrimSpace(doc.Assemble()) == "" {
			t.Errorf("scaffolded %s document assembles to empty text", kind)
		}
	}
}
