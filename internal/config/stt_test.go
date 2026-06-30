package config

import "testing"

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
