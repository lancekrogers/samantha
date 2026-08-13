package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

// promptReloader re-resolves a prompt document at the start of every turn so an
// edit to the file on disk takes effect on the next reply instead of the next
// restart.
//
// Two invariants shape this, and both are load-bearing:
//
//   - **Session identity stays pinned.** The reloader re-reads the document the
//     session is already bound to; it never consults the live config to decide
//     *which* document that is. Switching or editing a different persona must
//     not change the identity of a conversation already in flight — see
//     internal/persona/binding.go.
//
//   - **Unchanged content must stay byte-identical.** Ollama's system prompt
//     feeds a server-side prefix cache; handing back a newly-built but equal
//     string would still be equal, but rebuilding the *assembled* prompt (which
//     splices in the skills catalog) on every turn would be wasted work. So the
//     reloader compares the resolved document against the last one and returns
//     the cached value unless it actually changed.
//
// On a resolution error the last good value is kept. A half-saved YAML file
// mid-conversation must not end the turn; the caller reports the error and
// keeps speaking with the prompt it already had.
type promptReloader struct {
	mu sync.Mutex

	kind prompts.Kind
	// name is the document this reloader is bound to. It is captured once and
	// never re-read from config, which is what keeps identity pinned.
	name string

	lastGood string
	lastHash string
	// onChange is called when the resolved content differs from the previous
	// turn, with the new short hash. Used for the "is the model seeing my edit?"
	// log line.
	onChange func(hash string)
}

func newPromptReloader(kind prompts.Kind, name, initial string, onChange func(string)) *promptReloader {
	return &promptReloader{
		kind:     kind,
		name:     name,
		lastGood: initial,
		lastHash: shortPromptHash(initial),
		onChange: onChange,
	}
}

// resolve returns the current document text plus a flag reporting whether it
// changed since the previous call. The error is advisory: text is always
// usable, and is the last good value when resolution failed.
func (r *promptReloader) resolve(cfg *config.Config) (text string, changed bool, err error) {
	if r == nil {
		return "", false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	resolved, resolveErr := resolvePrompt(cfg, r.kind, r.name)
	if resolveErr != nil {
		// Keep last good. A broken edit must not brick a live conversation.
		return r.lastGood, false, fmt.Errorf("keeping previous %s prompt: %w", r.kind, resolveErr)
	}
	if resolved == r.lastGood {
		return r.lastGood, false, nil
	}

	r.lastGood = resolved
	r.lastHash = shortPromptHash(resolved)
	if r.onChange != nil {
		r.onChange(r.lastHash)
	}
	return resolved, true, nil
}

// hash returns the short hash of the current document, for diagnostics.
func (r *promptReloader) hash() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastHash
}

// shortPromptHash is a stable, human-comparable identifier for prompt content.
// The same value is surfaced by the CLI so "is the model seeing my edit?" is
// answerable by comparing two strings.
func shortPromptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
