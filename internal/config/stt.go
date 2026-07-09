package config

import (
	"fmt"
	"slices"
	"strings"
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
