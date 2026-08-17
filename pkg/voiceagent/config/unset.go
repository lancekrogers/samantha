package config

import (
	"fmt"
	"os"
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
	release, err := openConfigForWrite(path, spec.Key)
	if err != nil {
		return SetResult{}, err
	}
	defer release()

	data, existed, doc, err := readConfigDocument(path, spec.Key)
	if err != nil {
		return SetResult{}, err
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
	result.BackupPath = backupPath
	result.Changed = true
	result.Value = effectiveAfterUnset(spec)
	return result, nil
}

// effectiveAfterUnset re-reads the file — the truth again, now that the key is
// gone — and reports what the key resolves to. Usually that is the schema
// default, but a key with an environment binding resolves to the exported
// value, and reporting the default there would be a lie in a payload whose
// `value` field means "what this key holds now".
//
// It cannot ask viper for the answer. SetKeyFile installs a viper override for
// every key it writes, and viper has no way to remove one — a later
// ReadInConfig cannot outrank it. So a long-lived process (the TUI, the Mac
// agent, serve) that set a key and then unset it kept reading the value that
// had just been deleted from the file, while Source correctly said "default".
// The value is therefore computed from the file, the environment and the
// schema, and the stale override is overwritten with it. Found by adversarial
// review.
func effectiveAfterUnset(spec KeySpec) any {
	if _, err := LoadRaw(); err != nil {
		// The write already landed and the patched document was parsed before
		// it was written, so a load failure here says something about the rest
		// of the file, not about this key. The schema default is the honest
		// answer when the file cannot be believed.
		Set(spec.Key, spec.Default)
		return spec.Default
	}
	value := spec.Default
	if env := envBindings[spec.Key]; env != "" {
		if raw, ok := os.LookupEnv(env); ok {
			value = raw
			if coerced, err := coerceToSpec(spec, raw); err == nil {
				value = coerced
			}
		}
	}
	Set(spec.Key, value)
	return value
}

// removeKeyFromFile patches the source text, checks the key really is gone,
// and replaces the file atomically behind a backup.
func removeKeyFromFile(data []byte, doc *yaml.Node, segments []string, path, key string) (string, error) {
	patched, removed, err := deleteConfigKey(data, doc, segments)
	if err != nil {
		return "", writeOrShapeFailed(key, "updating config", err)
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
