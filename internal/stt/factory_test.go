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

func TestNewProviderRejectsConflictingSTTMode(t *testing.T) {
	cfg := &config.Config{STTProvider: "sherpa-streaming", STTMode: "offline"}

	_, _, err := NewProvider(cfg, nil, nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want stt_mode conflict error")
	}
	if !strings.Contains(err.Error(), "conflicts with stt_mode") {
		t.Fatalf("NewProvider() error = %q, want stt_mode conflict message", err)
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
// alias and provider+mode pair should fail at its own provider-named capture
// guard.
func TestNewProviderRoutesConfiguredAliases(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		mode     string
		wantMsg  string
	}{
		{name: "default", provider: "", wantMsg: "sherpa STT requires audio capture"},
		{name: "sherpa", provider: "sherpa", wantMsg: "sherpa STT requires audio capture"},
		{name: "sherpa-offline", provider: "sherpa-offline", wantMsg: "sherpa STT requires audio capture"},
		{name: "sherpa-streaming", provider: "sherpa-streaming", wantMsg: "sherpa-streaming STT requires audio capture"},
		{name: "whispercpp", provider: "whispercpp", wantMsg: "whispercpp STT requires audio capture"},
		{name: "sherpa with offline mode", provider: "sherpa", mode: "offline", wantMsg: "sherpa STT requires audio capture"},
		{name: "sherpa with streaming mode", provider: "sherpa", mode: "streaming", wantMsg: "sherpa-streaming STT requires audio capture"},
		{name: "whispercpp with cli mode", provider: "whispercpp", mode: "cli", wantMsg: "whispercpp STT requires audio capture"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{STTProvider: tt.provider, STTMode: tt.mode}

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
