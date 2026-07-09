package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Normalized STT provider and mode identifiers resolved from the single
// stt_provider config value.
const (
	STTProviderSherpa     = "sherpa"
	STTProviderWhisperCPP = "whispercpp"

	STTModeOffline   = "offline"
	STTModeStreaming = "streaming"
	STTModeCLI       = "cli"
)

// NormalizedSTT is the explicit provider/mode pair resolved from the single
// stt_provider config value. Alias preserves the trimmed, lower-cased
// configured value for display and config compatibility; it is empty when the
// default was used.
type NormalizedSTT struct {
	Provider string // sherpa, whispercpp
	Mode     string // offline, streaming, cli
	Alias    string // canonical configured value, empty when defaulted
}

// STTConfigMigrationProposal describes how to rewrite STT config into the
// explicit stt_provider + stt_mode schema without mutating files.
type STTConfigMigrationProposal struct {
	ConfigPath       string
	CurrentAlias     string
	ProposedProvider string
	ProposedMode     string
	Noop             bool
}

// STTConfigMigrationResult reports the outcome of applying a migration.
type STTConfigMigrationResult struct {
	STTConfigMigrationProposal
	BackupPath string
	Wrote      bool
}

// sttAliasTable maps every accepted stt_provider value to its normalized
// provider/mode. The empty key is the default: the reliable utterance-final
// sherpa path. sherpa-offline is a legacy alias for the same provider/mode.
var sttAliasTable = map[string]NormalizedSTT{
	"":                 {Provider: STTProviderSherpa, Mode: STTModeOffline},
	"sherpa":           {Provider: STTProviderSherpa, Mode: STTModeOffline},
	"sherpa-offline":   {Provider: STTProviderSherpa, Mode: STTModeOffline},
	"sherpa-streaming": {Provider: STTProviderSherpa, Mode: STTModeStreaming},
	"whispercpp":       {Provider: STTProviderWhisperCPP, Mode: STTModeCLI},
}

// sttProviderModes lists the stt_mode values each normalized provider accepts.
var sttProviderModes = map[string][]string{
	STTProviderSherpa:     {STTModeOffline, STTModeStreaming},
	STTProviderWhisperCPP: {STTModeCLI},
}

// sttModeLockedAliases are compound stt_provider aliases that already encode a
// mode; a conflicting stt_mode is a config error rather than a silent override.
var sttModeLockedAliases = map[string]bool{
	"sherpa-offline":   true,
	"sherpa-streaming": true,
}

// NormalizeSTT resolves a configured stt_provider value into an explicit
// provider and mode. ok is false for unsupported values. It never mutates or
// persists user config; aliases are mapped in memory only.
func NormalizeSTT(configured string) (norm NormalizedSTT, ok bool) {
	alias := strings.ToLower(strings.TrimSpace(configured))
	norm, ok = sttAliasTable[alias]
	if !ok {
		return NormalizedSTT{}, false
	}
	norm.Alias = alias
	return norm, true
}

// NormalizeSTTWithMode resolves the preferred stt_provider/stt_mode pair into
// an explicit provider and mode. An empty mode preserves legacy stt_provider
// behavior exactly (see NormalizeSTT). A non-empty mode must be valid for the
// resolved provider and must not conflict with a compound alias such as
// sherpa-streaming; errors name the setting to fix. Like NormalizeSTT, it never
// mutates or persists user config.
func NormalizeSTTWithMode(provider, mode string) (NormalizedSTT, error) {
	norm, ok := NormalizeSTT(provider)
	if !ok {
		return NormalizedSTT{}, fmt.Errorf("unsupported stt_provider %q; set stt_provider to sherpa, sherpa-streaming, or whispercpp", provider)
	}

	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return norm, nil
	}

	modes := sttProviderModes[norm.Provider]
	if !slices.Contains(modes, m) {
		return NormalizedSTT{}, fmt.Errorf("stt_mode %q is not supported by stt_provider %q; set stt_mode to %s, or remove stt_mode", m, norm.Provider, strings.Join(modes, " or "))
	}
	if sttModeLockedAliases[norm.Alias] && norm.Mode != m {
		return NormalizedSTT{}, fmt.Errorf("stt_provider %q already selects mode %q, which conflicts with stt_mode %q; set stt_provider=%s to use stt_mode, or remove stt_mode", norm.Alias, norm.Mode, m, norm.Provider)
	}
	if norm.Mode == m {
		return norm, nil
	}

	// The mode refines a bare provider (e.g. sherpa + streaming); normalize to
	// the same result as the corresponding compound alias.
	norm.Mode = m
	norm.Alias = norm.Provider + "-" + m
	return norm, nil
}

// ProposeSTTConfigMigration returns the explicit STT config values that would
// preserve the currently resolved provider/mode. It is read-only.
func ProposeSTTConfigMigration(cfg *Config, configPath string) (STTConfigMigrationProposal, error) {
	norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		return STTConfigMigrationProposal{}, err
	}
	currentAlias := norm.Alias
	if currentAlias == "" {
		currentAlias = "(default)"
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.STTProvider))
	mode := strings.ToLower(strings.TrimSpace(cfg.STTMode))
	return STTConfigMigrationProposal{
		ConfigPath:       configPath,
		CurrentAlias:     currentAlias,
		ProposedProvider: norm.Provider,
		ProposedMode:     norm.Mode,
		Noop:             provider == norm.Provider && mode == norm.Mode,
	}, nil
}

// WriteSTTConfigMigration applies the explicit STT provider/mode migration to
// configPath. It creates a timestamped backup before replacing an existing
// config file and writes through a temporary file before rename.
func WriteSTTConfigMigration(cfg *Config, configPath string) (STTConfigMigrationResult, error) {
	proposal, err := ProposeSTTConfigMigration(cfg, configPath)
	if err != nil {
		return STTConfigMigrationResult{}, err
	}
	result := STTConfigMigrationResult{STTConfigMigrationProposal: proposal}
	if proposal.Noop {
		return result, nil
	}

	data, exists, err := readOptionalFile(configPath)
	if err != nil {
		return STTConfigMigrationResult{}, err
	}
	doc, err := migrationYAMLDocument(data)
	if err != nil {
		return STTConfigMigrationResult{}, err
	}
	setYAMLScalar(doc.Content[0], "stt_provider", proposal.ProposedProvider)
	setYAMLScalar(doc.Content[0], "stt_mode", proposal.ProposedMode)

	out, err := encodeYAMLDocument(doc)
	if err != nil {
		return STTConfigMigrationResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return STTConfigMigrationResult{}, fmt.Errorf("creating config dir: %w", err)
	}
	if exists {
		backupPath, err := backupFile(configPath)
		if err != nil {
			return STTConfigMigrationResult{}, err
		}
		result.BackupPath = backupPath
	}
	if err := writeFileAtomic(configPath, out); err != nil {
		return STTConfigMigrationResult{}, err
	}
	result.Wrote = true
	return result, nil
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading config: %w", err)
	}
	return data, true, nil
}

func migrationYAMLDocument(data []byte) (*yaml.Node, error) {
	if strings.TrimSpace(string(data)) == "" {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config file must contain a YAML mapping")
	}
	return &doc, nil
}

func setYAMLScalar(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func encodeYAMLDocument(doc *yaml.Node) ([]byte, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	return []byte(b.String()), nil
}

func backupFile(path string) (string, error) {
	backupPath := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	src, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening config for backup: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating backup: %w", err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("writing backup: %w", err)
	}
	return backupPath, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("setting temp config permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}
	return nil
}
