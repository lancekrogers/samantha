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
// It is the single writer shared by the CLI, the TUI and the Mac app. A no-op
// write (the value already in the file) reports Changed=false and touches
// nothing, so an optimistic front-end toggle cannot churn the file.
func SetKeyFile(key, raw string) (SetResult, error) {
	spec, err := resolveSpec(key, false)
	if err != nil {
		return SetResult{}, err
	}
	value, err := coerceToSpec(spec, raw)
	if err != nil {
		return SetResult{}, err
	}
	return writeKeyToFile(spec, value)
}

// SetKeyFileValue is SetKeyFile for callers that already hold a typed value
// (the TUI's Settings screens). The value is validated and normalized against
// the key's schema type before it reaches the file.
func SetKeyFileValue(key string, value any) (SetResult, error) {
	return setKeyFileValue(key, value, false)
}

// SetManagedKey writes a key the schema marks non-editable, on behalf of the
// verb that owns it. `persona use` owns active_persona and persona: the flag
// means "not a generic Settings control", not "never written".
func SetManagedKey(key string, value any) error {
	_, err := setKeyFileValue(key, value, true)
	return err
}

func setKeyFileValue(key string, value any, allowManaged bool) (SetResult, error) {
	spec, err := resolveSpec(key, allowManaged)
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
func resolveSpec(key string, allowManaged bool) (KeySpec, error) {
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
	if !spec.Editable && !allowManaged {
		return KeySpec{}, &SetError{
			Code:    CodeNotEditable,
			Key:     spec.Key,
			Message: fmt.Sprintf("%s is managed by `samantha %s`", spec.Key, spec.ManagedBy),
		}
	}
	return spec, nil
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
	preserveBlankLines(doc, data)
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

	if err := setYAMLValue(mapping, segments, value); err != nil {
		return SetResult{}, writeFailed(spec.Key, "updating config", err)
	}
	out, err := encodeYAMLDocument(doc)
	if err != nil {
		return SetResult{}, writeFailed(spec.Key, "encoding config", err)
	}
	if existed {
		backupPath, backupErr := backupFile(path)
		if backupErr != nil {
			return SetResult{}, writeFailed(spec.Key, "backing up config", backupErr)
		}
		result.BackupPath = backupPath
		pruneBackups(path, keptBackups)
	}
	if err := writeFileAtomic(path, out); err != nil {
		return SetResult{}, writeFailed(spec.Key, "replacing config", err)
	}

	Set(spec.Key, value)
	result.Changed = true
	return result, nil
}

func writeFailed(key, what string, cause error) error {
	return &SetError{
		Code:    CodeWriteFailed,
		Key:     key,
		Message: fmt.Sprintf("%s: %v", what, cause),
		cause:   cause,
	}
}
