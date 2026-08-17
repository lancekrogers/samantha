package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Error codes reported by the single-key writer. They are wire values: the CLI
// prints them in its --json error payload and front ends branch on them.
const (
	CodeUnknownKey   = "unknown_key"
	CodeNotEditable  = "not_editable"
	CodeInvalidValue = "invalid_value"
	CodeLocked       = "locked"
	CodeParseFailed  = "parse_failed"
	CodeWriteFailed  = "write_failed"
)

// SetError is a machine-readable failure from the config write path.
type SetError struct {
	Code       string
	Key        string
	Message    string
	DidYouMean []string
	cause      error
}

func (e *SetError) Error() string { return e.Message }
func (e *SetError) Unwrap() error { return e.cause }

// SetResult describes one completed (or deliberately skipped) key write.
type SetResult struct {
	Key             string    `json:"key"`
	OldValue        any       `json:"old_value"`
	Value           any       `json:"value"`
	Type            ValueType `json:"type"`
	ConfigPath      string    `json:"config_file"`
	BackupPath      string    `json:"backup,omitempty"`
	Changed         bool      `json:"changed"`
	RestartRequired bool      `json:"restart_required"`
}

// fileWriteMu serializes writers inside this process so two goroutines queue
// instead of one losing the advisory lock race and reporting CodeLocked.
var fileWriteMu sync.Mutex

// SetKeyFile writes one key into config.yaml, surgically: the raw string is
// coerced by the key's schema type (never by whatever type happens to be in the
// file), and only that key's line changes — comments and key order survive.
//
// This is the string-valued, user-facing door: `samantha config set` and the
// legacy two-argument form. It refuses a key the schema marks non-editable,
// naming the verb that owns it, because a generic setter is exactly where a
// user would otherwise corrupt state another command maintains.
//
// A no-op write (the value already in the file) reports Changed=false and
// touches nothing, so an optimistic front-end toggle cannot churn the file.
func SetKeyFile(key, raw string) (SetResult, error) {
	spec, err := resolveSpec(key, true)
	if err != nil {
		return SetResult{}, err
	}
	value, err := coerceToSpec(spec, raw)
	if err != nil {
		return SetResult{}, err
	}
	return writeKeyToFile(spec, value)
}

// SetKeyFileValue is SetKeyFile for in-process callers that already hold a
// typed value: the TUI's Settings screens, `persona use`, the meeting
// destination editor. The value is validated and normalized against the key's
// schema type before it reaches the file.
//
// It deliberately does NOT enforce KeySpec.Editable. That flag answers "may a
// generic Settings control or `config set` change this?", and the answer is no
// precisely because some other code owns the key — the TUI owns
// tui_mouse_enabled, `persona use` owns active_persona, the meeting editor owns
// meeting.route.destinations. Those owners are these callers; refusing them
// would break the feature the flag exists to protect.
func SetKeyFileValue(key string, value any) (SetResult, error) {
	spec, err := resolveSpec(key, false)
	if err != nil {
		return SetResult{}, err
	}
	normalized, err := normalizeToSpec(spec, value)
	if err != nil {
		return SetResult{}, err
	}
	return writeKeyToFile(spec, normalized)
}

// resolveSpec resolves key to a spec the caller is allowed to write.
// enforceEditable is set by the string CLI path only; see SetKeyFileValue.
func resolveSpec(key string, enforceEditable bool) (KeySpec, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	spec, ok := SpecFor(key)
	if !ok {
		return KeySpec{}, &SetError{
			Code:       CodeUnknownKey,
			Key:        key,
			Message:    fmt.Sprintf("unknown config key %q", key),
			DidYouMean: SuggestKeys(key),
		}
	}
	if enforceEditable && !spec.Editable {
		return KeySpec{}, &SetError{
			Code:    CodeNotEditable,
			Key:     spec.Key,
			Message: fmt.Sprintf("%s is managed by `%s`", spec.Key, managedByCommand(spec.ManagedBy)),
		}
	}
	return spec, nil
}

// managedByCommand renders a ManagedBy verb as the command a user would type.
// The verbs are written as the user says them ("persona use", "samantha TUI"),
// so the samantha prefix is added only when it is not already there — without
// this, tui_mouse_enabled reported "samantha samantha TUI".
func managedByCommand(managedBy string) string {
	managedBy = strings.TrimSpace(managedBy)
	if managedBy == "" {
		return "samantha"
	}
	if strings.HasPrefix(strings.ToLower(managedBy), "samantha") {
		return managedBy
	}
	return "samantha " + managedBy
}

// SuggestKeys returns up to five known keys that share a prefix with, or
// contain, the given input. Used for did_you_mean on an unknown key.
func SuggestKeys(key string) []string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return nil
	}
	var out []string
	for _, candidate := range SchemaKeys() {
		if strings.HasPrefix(candidate, key) || strings.Contains(candidate, key) {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// writeKeyToFile performs the read-modify-write: lock, parse, compare, patch,
// back up, replace atomically, then refresh the in-process viper so a later Get
// in the same process agrees with the file.
func writeKeyToFile(spec KeySpec, value any) (SetResult, error) {
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()

	path := ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return SetResult{}, writeFailed(spec.Key, "creating config dir", err)
	}
	release, err := acquireConfigLock(path)
	if err != nil {
		return SetResult{}, err
	}
	defer release()

	data, existed, err := readOptionalFile(path)
	if err != nil {
		return SetResult{}, &SetError{Code: CodeParseFailed, Key: spec.Key, Message: err.Error(), cause: err}
	}
	doc, err := migrationYAMLDocument(data)
	if err != nil {
		return SetResult{}, &SetError{Code: CodeParseFailed, Key: spec.Key, Message: err.Error(), cause: err}
	}
	mapping := doc.Content[0]

	segments := strings.Split(spec.Key, ".")
	result := SetResult{
		Key:             spec.Key,
		OldValue:        spec.Default,
		Value:           value,
		Type:            spec.Type,
		ConfigPath:      path,
		RestartRequired: spec.RestartRequired,
	}
	// The old value is what the file holds, falling back to the schema default.
	// An env override is deliberately ignored here: `set` changes the file, and
	// letting an exported variable mask a pending file change would silently
	// drop the write.
	if current, found := yamlValueAt(mapping, segments); found {
		result.OldValue = current
	}
	if sameValue(result.OldValue, value) {
		return result, nil
	}

	patched, err := patchConfigSource(data, doc, segments, value)
	if err != nil {
		return SetResult{}, writeFailed(spec.Key, "updating config", err)
	}
	// Refresh the in-process value from the patched text rather than from the Go
	// value: a later Get in this process then sees exactly what a fresh Load
	// would, including for structured keys whose Go type has no JSON tags. It is
	// also the writer's own check that the edit it made still parses and still
	// holds what it was asked to write — a text edit that did not land is
	// refused here instead of being saved over the user's config.
	refreshed, err := verifyPatched(patched, segments, value)
	if err != nil {
		return SetResult{}, writeFailed(spec.Key, "updating config", err)
	}
	backupPath, err := backupAndReplace(path, patched, existed, spec.Key)
	if err != nil {
		return SetResult{}, err
	}
	Set(spec.Key, refreshed)
	result.BackupPath = backupPath
	result.Changed = true
	return result, nil
}

// verifyPatched re-reads the patched text and returns the value it now holds at
// the path. A patch that no longer parses, or that did not land the value, is
// an error rather than something to write over a working config.
func verifyPatched(patched []byte, segments []string, value any) (any, error) {
	doc, err := migrationYAMLDocument(patched)
	if err != nil {
		return nil, err
	}
	got, ok := yamlValueAt(doc.Content[0], segments)
	if !ok || !sameValue(got, value) {
		return nil, fmt.Errorf("the edited config does not hold %s", strings.Join(segments, "."))
	}
	return got, nil
}

// backupAndReplace writes the patched document over path, keeping a backup of
// what was there. The replacement is atomic, so a crash mid-write leaves the
// old file intact rather than a truncated one.
func backupAndReplace(path string, patched []byte, existed bool, key string) (string, error) {
	var backupPath string
	if existed {
		var err error
		backupPath, err = backupFile(path)
		if err != nil {
			return "", writeFailed(key, "backing up config", err)
		}
		pruneBackups(path, keptBackups)
	}
	if err := writeFileAtomic(path, patched); err != nil {
		return "", writeFailed(key, "replacing config", err)
	}
	return backupPath, nil
}

func writeFailed(key, what string, cause error) error {
	return &SetError{
		Code:    CodeWriteFailed,
		Key:     key,
		Message: fmt.Sprintf("%s: %v", what, cause),
		cause:   cause,
	}
}
