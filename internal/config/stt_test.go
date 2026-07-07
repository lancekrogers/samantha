package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSTT(t *testing.T) {
	tests := []struct {
		name         string
		configured   string
		wantProvider string
		wantMode     string
		wantAlias    string
		wantOK       bool
	}{
		{name: "empty defaults to sherpa offline", configured: "", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "", wantOK: true},
		{name: "whitespace only defaults to sherpa offline", configured: "   ", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "", wantOK: true},
		{name: "sherpa keeps utterance-final offline", configured: "sherpa", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa", wantOK: true},
		{name: "sherpa-offline legacy alias", configured: "sherpa-offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa-offline", wantOK: true},
		{name: "sherpa-streaming explicit streaming", configured: "sherpa-streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming", wantOK: true},
		{name: "whispercpp cli", configured: "whispercpp", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI, wantAlias: "whispercpp", wantOK: true},
		{name: "surrounding whitespace trimmed", configured: "  sherpa-streaming  ", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming", wantOK: true},
		{name: "case folded alias", configured: "Sherpa-Offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa-offline", wantOK: true},
		{name: "mixed case whispercpp", configured: "WhisperCPP", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI, wantAlias: "whispercpp", wantOK: true},
		{name: "unsupported value", configured: "google", wantOK: false},
		{name: "unsupported uppercase value", configured: "DEEPGRAM", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeSTT(tt.configured)
			if ok != tt.wantOK {
				t.Fatalf("NormalizeSTT(%q) ok = %v, want %v", tt.configured, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if got != (NormalizedSTT{}) {
					t.Errorf("NormalizeSTT(%q) on unsupported = %+v, want zero value", tt.configured, got)
				}
				return
			}
			if got.Provider != tt.wantProvider {
				t.Errorf("NormalizeSTT(%q) Provider = %q, want %q", tt.configured, got.Provider, tt.wantProvider)
			}
			if got.Mode != tt.wantMode {
				t.Errorf("NormalizeSTT(%q) Mode = %q, want %q", tt.configured, got.Mode, tt.wantMode)
			}
			if got.Alias != tt.wantAlias {
				t.Errorf("NormalizeSTT(%q) Alias = %q, want %q", tt.configured, got.Alias, tt.wantAlias)
			}
		})
	}
}

func TestNormalizeSTTWithMode(t *testing.T) {
	tests := []struct {
		name            string
		provider        string
		mode            string
		wantErrContains string
		wantProvider    string
		wantMode        string
		wantAlias       string
	}{
		// Error cases first.
		{name: "unsupported provider", provider: "google", mode: "", wantErrContains: "unsupported stt_provider"},
		{name: "unsupported provider with mode", provider: "deepgram", mode: "streaming", wantErrContains: "unsupported stt_provider"},
		{name: "sherpa-streaming conflicts with offline mode", provider: "sherpa-streaming", mode: "offline", wantErrContains: `set stt_provider=sherpa to use stt_mode, or remove stt_mode`},
		{name: "sherpa-offline conflicts with streaming mode", provider: "sherpa-offline", mode: "streaming", wantErrContains: `set stt_provider=sherpa to use stt_mode, or remove stt_mode`},
		{name: "sherpa rejects cli mode", provider: "sherpa", mode: "cli", wantErrContains: "set stt_mode to offline or streaming"},
		{name: "whispercpp rejects offline mode", provider: "whispercpp", mode: "offline", wantErrContains: "set stt_mode to cli"},
		{name: "whispercpp rejects streaming mode", provider: "whispercpp", mode: "streaming", wantErrContains: "set stt_mode to cli"},
		{name: "unknown mode value", provider: "sherpa", mode: "batch", wantErrContains: "set stt_mode to offline or streaming"},

		// Empty mode preserves legacy alias behavior.
		{name: "empty mode defaults", provider: "", mode: "", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: ""},
		{name: "empty mode sherpa", provider: "sherpa", mode: "", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa"},
		{name: "empty mode sherpa-offline alias", provider: "sherpa-offline", mode: "", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa-offline"},
		{name: "empty mode sherpa-streaming alias", provider: "sherpa-streaming", mode: "", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},
		{name: "empty mode whispercpp", provider: "whispercpp", mode: "", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI, wantAlias: "whispercpp"},
		{name: "whitespace mode treated as empty", provider: "sherpa-streaming", mode: "   ", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},

		// Preferred provider/mode schema.
		{name: "sherpa offline", provider: "sherpa", mode: "offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa"},
		{name: "sherpa streaming matches compound alias", provider: "sherpa", mode: "streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},
		{name: "whispercpp cli", provider: "whispercpp", mode: "cli", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI, wantAlias: "whispercpp"},
		{name: "compound alias with agreeing mode", provider: "sherpa-streaming", mode: "streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},
		{name: "sherpa-offline with agreeing mode", provider: "sherpa-offline", mode: "offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline, wantAlias: "sherpa-offline"},
		{name: "default provider with streaming mode", provider: "", mode: "streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},
		{name: "mode trimmed and case folded", provider: "sherpa", mode: "  Streaming ", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming, wantAlias: "sherpa-streaming"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSTTWithMode(tt.provider, tt.mode)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("NormalizeSTTWithMode(%q, %q) error = nil, want containing %q", tt.provider, tt.mode, tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("NormalizeSTTWithMode(%q, %q) error = %q, want containing %q", tt.provider, tt.mode, err, tt.wantErrContains)
				}
				if got != (NormalizedSTT{}) {
					t.Errorf("NormalizeSTTWithMode(%q, %q) on error = %+v, want zero value", tt.provider, tt.mode, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeSTTWithMode(%q, %q) error: %v", tt.provider, tt.mode, err)
			}
			want := NormalizedSTT{Provider: tt.wantProvider, Mode: tt.wantMode, Alias: tt.wantAlias}
			if got != want {
				t.Errorf("NormalizeSTTWithMode(%q, %q) = %+v, want %+v", tt.provider, tt.mode, got, want)
			}
		})
	}
}

// TestNormalizeSTTWithModeMatchesLegacyAliases proves the preferred schema and
// the compound alias resolve to the same result, and that empty stt_mode is
// byte-for-byte identical to NormalizeSTT.
func TestNormalizeSTTWithModeMatchesLegacyAliases(t *testing.T) {
	fromMode, err := NormalizeSTTWithMode("sherpa", "streaming")
	if err != nil {
		t.Fatalf("NormalizeSTTWithMode(sherpa, streaming) error: %v", err)
	}
	fromAlias, ok := NormalizeSTT("sherpa-streaming")
	if !ok {
		t.Fatal("NormalizeSTT(sherpa-streaming) ok = false, want true")
	}
	if fromMode != fromAlias {
		t.Errorf("preferred schema = %+v, legacy alias = %+v, want equal", fromMode, fromAlias)
	}

	for _, alias := range []string{"", "sherpa", "sherpa-offline", "sherpa-streaming", "whispercpp"} {
		legacy, ok := NormalizeSTT(alias)
		if !ok {
			t.Fatalf("NormalizeSTT(%q) ok = false, want true", alias)
		}
		got, err := NormalizeSTTWithMode(alias, "")
		if err != nil {
			t.Fatalf("NormalizeSTTWithMode(%q, \"\") error: %v", alias, err)
		}
		if got != legacy {
			t.Errorf("NormalizeSTTWithMode(%q, \"\") = %+v, NormalizeSTT = %+v, want equal", alias, got, legacy)
		}
	}
}

// TestNormalizeSTTDoesNotMutateInput guards the migration rule that
// normalization is purely in-memory and never rewrites the configured value.
func TestNormalizeSTTDoesNotMutateInput(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa-offline"}
	if _, ok := NormalizeSTT(cfg.STTProvider); !ok {
		t.Fatalf("NormalizeSTT(%q) ok = false, want true", cfg.STTProvider)
	}
	if cfg.STTProvider != "sherpa-offline" {
		t.Errorf("NormalizeSTT mutated config: STTProvider = %q, want %q", cfg.STTProvider, "sherpa-offline")
	}
}

// TestLoadAndNormalizeSTTAliases is a config-boundary regression test: it loads
// config with the default and with each supported alias via the STT_PROVIDER
// env override, then proves the loaded value normalizes to the expected
// provider/mode. This guards that existing configs keep routing as before.
func TestLoadAndNormalizeSTTAliases(t *testing.T) {
	tests := []struct {
		name         string
		setEnv       bool
		envValue     string
		wantConfig   string
		wantProvider string
		wantMode     string
	}{
		{name: "default unset routes to sherpa offline", setEnv: false, wantConfig: "sherpa", wantProvider: STTProviderSherpa, wantMode: STTModeOffline},
		{name: "sherpa routes to offline", setEnv: true, envValue: "sherpa", wantConfig: "sherpa", wantProvider: STTProviderSherpa, wantMode: STTModeOffline},
		{name: "sherpa-offline alias routes to offline", setEnv: true, envValue: "sherpa-offline", wantConfig: "sherpa-offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline},
		{name: "sherpa-streaming routes to streaming", setEnv: true, envValue: "sherpa-streaming", wantConfig: "sherpa-streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming},
		{name: "whispercpp routes to cli", setEnv: true, envValue: "whispercpp", wantConfig: "whispercpp", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := configFile
			configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
			defer func() { configFile = orig }()

			setDefaults(v)
			if tt.setEnv {
				t.Setenv("STT_PROVIDER", tt.envValue)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.STTProvider != tt.wantConfig {
				t.Fatalf("cfg.STTProvider = %q, want %q", cfg.STTProvider, tt.wantConfig)
			}

			norm, ok := NormalizeSTT(cfg.STTProvider)
			if !ok {
				t.Fatalf("NormalizeSTT(%q) ok = false, want true", cfg.STTProvider)
			}
			if norm.Provider != tt.wantProvider || norm.Mode != tt.wantMode {
				t.Fatalf("NormalizeSTT(%q) = %s/%s, want %s/%s", cfg.STTProvider, norm.Provider, norm.Mode, tt.wantProvider, tt.wantMode)
			}
		})
	}
}

// TestLoadSTTModeEnvBinding proves the STT_MODE env override reaches the
// loaded config and normalizes with stt_provider through the preferred schema.
func TestLoadSTTModeEnvBinding(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		mode         string
		wantProvider string
		wantMode     string
	}{
		{name: "sherpa with streaming mode", provider: "sherpa", mode: "streaming", wantProvider: STTProviderSherpa, wantMode: STTModeStreaming},
		{name: "sherpa with offline mode", provider: "sherpa", mode: "offline", wantProvider: STTProviderSherpa, wantMode: STTModeOffline},
		{name: "whispercpp with cli mode", provider: "whispercpp", mode: "cli", wantProvider: STTProviderWhisperCPP, wantMode: STTModeCLI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := configFile
			configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
			defer func() { configFile = orig }()

			setDefaults(v)
			t.Setenv("STT_PROVIDER", tt.provider)
			t.Setenv("STT_MODE", tt.mode)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.STTMode != tt.mode {
				t.Fatalf("cfg.STTMode = %q, want %q", cfg.STTMode, tt.mode)
			}

			norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
			if err != nil {
				t.Fatalf("NormalizeSTTWithMode(%q, %q) error: %v", cfg.STTProvider, cfg.STTMode, err)
			}
			if norm.Provider != tt.wantProvider || norm.Mode != tt.wantMode {
				t.Fatalf("NormalizeSTTWithMode(%q, %q) = %s/%s, want %s/%s", cfg.STTProvider, cfg.STTMode, norm.Provider, norm.Mode, tt.wantProvider, tt.wantMode)
			}
		})
	}
}

// TestLoadSTTModeFromConfigFile proves stt_mode also survives the config file
// path and that neither value is rewritten on load.
func TestLoadSTTModeFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("stt_provider: sherpa\nstt_mode: streaming\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := configFile
	configFile = path
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.STTProvider != "sherpa" || cfg.STTMode != "streaming" {
		t.Fatalf("loaded stt_provider/stt_mode = %q/%q, want sherpa/streaming (preserved on load)", cfg.STTProvider, cfg.STTMode)
	}

	norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		t.Fatalf("NormalizeSTTWithMode error: %v", err)
	}
	if norm.Provider != STTProviderSherpa || norm.Mode != STTModeStreaming {
		t.Fatalf("NormalizeSTTWithMode(sherpa, streaming) = %+v, want sherpa/streaming", norm)
	}
}

// TestLoadSTTProviderFromConfigFile proves the alias also survives the config
// file path (not just env) and that the configured alias is preserved, never
// rewritten on load.
func TestLoadSTTProviderFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("stt_provider: sherpa-offline\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := configFile
	configFile = path
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.STTProvider != "sherpa-offline" {
		t.Fatalf("cfg.STTProvider = %q, want sherpa-offline (alias preserved on load)", cfg.STTProvider)
	}

	norm, ok := NormalizeSTT(cfg.STTProvider)
	if !ok || norm.Provider != STTProviderSherpa || norm.Mode != STTModeOffline {
		t.Fatalf("NormalizeSTT(sherpa-offline) = %+v ok=%v, want sherpa/offline", norm, ok)
	}
	if norm.Alias != "sherpa-offline" {
		t.Fatalf("norm.Alias = %q, want sherpa-offline", norm.Alias)
	}
}
