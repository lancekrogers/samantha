package config

import (
	"os"
	"path/filepath"
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
