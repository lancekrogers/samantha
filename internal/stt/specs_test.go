package stt

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestSpecForNormalized(t *testing.T) {
	tests := []struct {
		name   string
		norm   config.NormalizedSTT
		want   Spec
		wantOK bool
	}{
		{
			name: "sherpa offline is utterance-final",
			norm: config.NormalizedSTT{Provider: config.STTProviderSherpa, Mode: config.STTModeOffline},
			want: Spec{
				Provider:       config.STTProviderSherpa,
				Mode:           config.STTModeOffline,
				EmitsPartial:   false,
				UsesEndpoint:   false,
				SupportsEOF:    true,
				RequiresVAD:    true,
				RecommendedUse: "default; reliable utterance-final transcription",
			},
			wantOK: true,
		},
		{
			name: "sherpa streaming emits partials and self-endpoints",
			norm: config.NormalizedSTT{Provider: config.STTProviderSherpa, Mode: config.STTModeStreaming},
			want: Spec{
				Provider:       config.STTProviderSherpa,
				Mode:           config.STTModeStreaming,
				EmitsPartial:   true,
				UsesEndpoint:   true,
				SupportsEOF:    true,
				RequiresVAD:    true,
				RecommendedUse: "live partial transcripts with recognizer endpointing",
			},
			wantOK: true,
		},
		{
			name: "whispercpp cli is utterance-final",
			norm: config.NormalizedSTT{Provider: config.STTProviderWhisperCPP, Mode: config.STTModeCLI},
			want: Spec{
				Provider:       config.STTProviderWhisperCPP,
				Mode:           config.STTModeCLI,
				EmitsPartial:   false,
				UsesEndpoint:   false,
				SupportsEOF:    true,
				RequiresVAD:    true,
				RecommendedUse: "utterance-final transcription via external whisper.cpp CLI",
			},
			wantOK: true,
		},
		{
			name:   "unknown provider yields no spec",
			norm:   config.NormalizedSTT{Provider: "google", Mode: config.STTModeStreaming},
			wantOK: false,
		},
		{
			name:   "known provider with wrong mode yields no spec",
			norm:   config.NormalizedSTT{Provider: config.STTProviderSherpa, Mode: config.STTModeCLI},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SpecForNormalized(tt.norm)
			if ok != tt.wantOK {
				t.Fatalf("SpecForNormalized(%+v) ok = %v, want %v", tt.norm, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if got != (Spec{}) {
					t.Errorf("SpecForNormalized(%+v) on unsupported = %+v, want zero value", tt.norm, got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("SpecForNormalized(%+v) = %+v, want %+v", tt.norm, got, tt.want)
			}
		})
	}
}

// TestConfiguredAliasesResolveToSpecs proves every configured alias the factory
// accepts (via config.NormalizeSTT, the same routing NewProvider uses) lands on
// the expected spec provider/mode.
func TestConfiguredAliasesResolveToSpecs(t *testing.T) {
	tests := []struct {
		alias        string
		wantProvider string
		wantMode     string
	}{
		{alias: "", wantProvider: config.STTProviderSherpa, wantMode: config.STTModeOffline},
		{alias: "sherpa", wantProvider: config.STTProviderSherpa, wantMode: config.STTModeOffline},
		{alias: "sherpa-offline", wantProvider: config.STTProviderSherpa, wantMode: config.STTModeOffline},
		{alias: "sherpa-streaming", wantProvider: config.STTProviderSherpa, wantMode: config.STTModeStreaming},
		{alias: "whispercpp", wantProvider: config.STTProviderWhisperCPP, wantMode: config.STTModeCLI},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			norm, ok := config.NormalizeSTT(tt.alias)
			if !ok {
				t.Fatalf("NormalizeSTT(%q) ok = false, want true", tt.alias)
			}

			spec, ok := SpecForNormalized(norm)
			if !ok {
				t.Fatalf("SpecForNormalized(%+v) ok = false, want true", norm)
			}
			if spec.Provider != tt.wantProvider {
				t.Errorf("spec.Provider = %q, want %q", spec.Provider, tt.wantProvider)
			}
			if spec.Mode != tt.wantMode {
				t.Errorf("spec.Mode = %q, want %q", spec.Mode, tt.wantMode)
			}
		})
	}
}

// TestEveryListedProviderHasSpec keeps the providers command coherent: each
// name the factory advertises must normalize to a capability spec.
func TestEveryListedProviderHasSpec(t *testing.T) {
	for _, provider := range Providers() {
		t.Run(provider.Name, func(t *testing.T) {
			norm, ok := config.NormalizeSTT(provider.Name)
			if !ok {
				t.Fatalf("NormalizeSTT(%q) ok = false, want true", provider.Name)
			}
			if _, ok := SpecForNormalized(norm); !ok {
				t.Errorf("SpecForNormalized(%+v) ok = false, want spec for listed provider %q", norm, provider.Name)
			}
		})
	}
}
