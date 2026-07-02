package stt

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{STTProvider: "google"}

	_, _, err := NewProvider(cfg, nil, nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported stt_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported stt_provider message", err)
	}
	for _, spec := range providerSpecs {
		if !strings.Contains(err.Error(), spec.Name) {
			t.Errorf("NewProvider() error = %q, missing provider %q from list", err, spec.Name)
		}
	}
}

func TestNewProviderRequiresVADForSherpa(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa"}

	_, _, err := NewProvider(cfg, nil, nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want missing dependency error")
	}
	if !strings.Contains(err.Error(), "requires audio capture") {
		t.Fatalf("NewProvider() error = %q, want capture requirement message", err)
	}
}

// TestNewProviderRoutesConfiguredAliases proves the normalized provider/mode
// reaches the matching construction branch. With nil capture every supported
// alias should fail at its own provider-named capture guard.
func TestNewProviderRoutesConfiguredAliases(t *testing.T) {
	tests := []struct {
		provider string
		wantMsg  string
	}{
		{provider: "", wantMsg: "sherpa STT requires audio capture"},
		{provider: "sherpa", wantMsg: "sherpa STT requires audio capture"},
		{provider: "sherpa-offline", wantMsg: "sherpa STT requires audio capture"},
		{provider: "sherpa-streaming", wantMsg: "sherpa-streaming STT requires audio capture"},
		{provider: "whispercpp", wantMsg: "whispercpp STT requires audio capture"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			cfg := &config.Config{STTProvider: tt.provider}

			_, _, err := NewProvider(cfg, nil, nil)
			if err == nil {
				t.Fatalf("NewProvider(%q) error = nil, want capture requirement error", tt.provider)
			}
			if err.Error() != tt.wantMsg {
				t.Fatalf("NewProvider(%q) error = %q, want %q", tt.provider, err, tt.wantMsg)
			}
		})
	}
}
