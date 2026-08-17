package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/prompts"
)

// Sentinel refusals, so a caller can map them to a machine-readable code
// without matching on message text.
var (
	// ErrBuiltinProtected refuses removal of the shipped persona. It is the
	// one profile every heal path falls back to.
	ErrBuiltinProtected = errors.New("built-in persona cannot be deleted")
	// ErrLastPersona refuses removal of the only profile on disk: config load
	// would then have nothing to resolve.
	ErrLastPersona = errors.New("cannot delete the only persona")
)

// DeleteResult reports what Delete removed, what it deliberately left behind,
// and where the active persona ended up.
type DeleteResult struct {
	// ID is the deleted persona.
	ID string
	// Removed lists the paths this call unlinked.
	Removed []string
	// Kept lists documents left in place because another persona owns them —
	// a UI should say so rather than imply a clean sweep.
	Kept []string
	// ActivePersona is the active id after the call.
	ActivePersona string
	// Reactivated reports whether the deleted persona was active and another
	// one had to be adopted.
	Reactivated bool
}

// Delete removes a persona profile directory and the prompt document that
// persona owns, then heals the active persona when the deleted one was it.
//
// Healing goes through Use, never a bare active_persona write: agent_name, the
// persona prompt ref, and the TTS keys all have to move together or the next
// config load speaks with one persona's name and another's voice.
func Delete(cfg *config.Config, id string) (*DeleteResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("persona: config is nil")
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	if p.Builtin {
		return nil, fmt.Errorf("persona %q: %w", id, ErrBuiltinProtected)
	}

	all, err := List()
	if err != nil {
		return nil, err
	}
	remaining := make([]*Profile, 0, len(all))
	for _, x := range all {
		if x.ID != id {
			remaining = append(remaining, x)
		}
	}
	if len(remaining) == 0 {
		return nil, fmt.Errorf("persona %q: %w", id, ErrLastPersona)
	}

	owned, shared := promptOwnership(p)
	res := &DeleteResult{ID: id, Removed: []string{}, Kept: []string{}, ActivePersona: ActiveID(cfg)}

	dir := filepath.Join(Dir(), id)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("removing persona %s: %w", dir, err)
	}
	res.Removed = append(res.Removed, dir)

	// The prompt goes second: a failed profile removal must not orphan the
	// document that identifies it.
	if owned != "" {
		if err := os.Remove(owned); err != nil && !os.IsNotExist(err) {
			return res, fmt.Errorf("removing persona prompt %s: %w", owned, err)
		}
		res.Removed = append(res.Removed, owned)
	}
	if shared != "" {
		res.Kept = append(res.Kept, shared)
	}

	if ActiveID(cfg) != id {
		return res, nil
	}
	next := pickFallbackID(remaining)
	if err := Use(cfg, next); err != nil {
		return res, fmt.Errorf("reactivating persona %q after deleting %q: %w", next, id, err)
	}
	res.ActivePersona = next
	res.Reactivated = true
	return res, nil
}

// promptOwnership splits p's prompt documents into the one this persona owns
// (and delete may remove) and a shared reference it must leave alone.
//
// Ownership is by file name: Create always writes prompts/persona/<id>.yaml,
// and no other persona can resolve that name. A prompts.persona pointing
// somewhere else is a deliberate shared reference — removing it would break
// the persona that does own it.
func promptOwnership(p *Profile) (owned, shared string) {
	if path, ok := userPromptPath(p.ID); ok {
		owned = path
	}
	ref := strings.TrimSpace(p.Prompts.Persona)
	if ref == "" || ref == p.ID {
		return owned, ""
	}
	if path, ok := userPromptPath(ref); ok {
		shared = path
	}
	return owned, shared
}

// userPromptPath reports the file backing a kind=persona document, but only
// when it is a real user document — an embedded default has no file to unlink.
func userPromptPath(name string) (string, bool) {
	entry, err := prompts.Describe(promptsDir(), prompts.KindPersona, name)
	if err != nil || entry.Source != prompts.SourceUser || entry.Path == "" {
		return "", false
	}
	return entry.Path, true
}
