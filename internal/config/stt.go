package config

import "strings"

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
