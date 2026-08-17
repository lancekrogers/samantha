package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnsetKeyFile removes one key from config.yaml so its built-in default — or
// its environment binding — takes over again.
//
// It is the way back that `set` alone cannot offer: a key whose value is the
// empty string reads identically to one that was never written, but only the
// second lets an env var or a future default change take effect. Removing the
// key is therefore not the same as setting it to "", and both exist.
//
// The removal is surgical in the same sense as SetKeyFile: only the lines that
// key occupies go, and a comment written above it stays where its author put
// it. Unknown keys are refused, and so are keys another verb owns — `config
// unset active_persona` would silently undo `persona use`.
//
// Removing a key the file does not hold is not an error: it reports
// Changed=false and touches nothing, so a front end's "reset to default" is
// idempotent.
func UnsetKeyFile(key string) (SetResult, error) {
	spec, err := resolveSpec(key, true)
	if err != nil {
		return SetResult{}, err
	}

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

	segments := strings.Split(spec.Key, ".")
	result := SetResult{
		Key:             spec.Key,
		OldValue:        spec.Default,
		Value:           spec.Default,
		Type:            spec.Type,
		ConfigPath:      path,
		RestartRequired: spec.RestartRequired,
	}
	current, found := yamlValueAt(doc.Content[0], segments)
	if !existed || !found {
		return result, nil
	}
	result.OldValue = current

	backupPath, err := removeKeyFromFile(data, doc, segments, path, spec.Key)
	if err != nil {
		return SetResult{}, err
	}

	// The file is the truth again, so it is re-read: a later Get or Source in
	// this process then reports the default (or the env binding) rather than
	// the value that was just removed. Best-effort — the write already
	// succeeded, and the patched document was parsed before it was written.
	_, _ = LoadRaw()

	result.BackupPath = backupPath
	result.Changed = true
	return result, nil
}

// removeKeyFromFile patches the source text, checks the key really is gone,
// and replaces the file atomically behind a backup.
func removeKeyFromFile(data []byte, doc *yaml.Node, segments []string, path, key string) (string, error) {
	patched, removed, err := deleteConfigKey(data, doc, segments)
	if err != nil {
		return "", writeFailed(key, "updating config", err)
	}
	if !removed {
		return "", writeFailed(key, "updating config", fmt.Errorf("%s was not found in the document", key))
	}
	if err := verifyUnset(patched, segments); err != nil {
		return "", writeFailed(key, "updating config", err)
	}
	return backupAndReplace(path, patched, true, key)
}

// verifyUnset re-reads the patched text and refuses a patch that did not remove
// the key, or that no longer parses — the same guard verifyPatched gives writes.
func verifyUnset(patched []byte, segments []string) error {
	doc, err := migrationYAMLDocument(patched)
	if err != nil {
		return err
	}
	if _, found := yamlValueAt(doc.Content[0], segments); found {
		return fmt.Errorf("the edited config still holds %s", strings.Join(segments, "."))
	}
	return nil
}
