package tts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
)

type contractProvider struct {
	available bool
	status    ProviderStatus
	cap       ProviderCapabilities
	request   func(context.Context, SynthesisRequest) (SynthesisResult, error)
}

func (p *contractProvider) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := p.SynthesizeRequest(ctx, SynthesisRequest{Text: text})
	return result.Stream, err
}
func (p *contractProvider) Available() bool                   { return p.available }
func (p *contractProvider) ListVoices(string, string) []Voice { return nil }
func (p *contractProvider) SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	return p.request(ctx, req)
}
func (p *contractProvider) Capabilities() ProviderCapabilities { return p.cap.Clone() }
func (p *contractProvider) Status() ProviderStatus             { return p.status }

var _ Provider = (*contractProvider)(nil)
var _ RequestProvider = (*contractProvider)(nil)
var _ CapabilityProvider = (*contractProvider)(nil)

func TestCapabilityProviderContractReportsModesAndStatus(t *testing.T) {
	p := &contractProvider{
		available: true,
		status:    ProviderStatus{Provider: "fake", Available: true, ModelReady: true},
		cap: ProviderCapabilities{
			Provider:   "fake",
			Model:      "fake-model",
			ModelReady: true,
			Modes: []VoiceModeCapability{{
				ID:                     VoiceModeApprovedClone,
				RequiresReferenceAudio: true,
				RequiresReferenceText:  true,
			}},
		},
		request: func(context.Context, SynthesisRequest) (SynthesisResult, error) {
			return SynthesisResult{Provider: "fake", Mode: VoiceModeApprovedClone}, nil
		},
	}

	status := p.Status()
	if !status.Available || !status.ModelReady {
		t.Fatalf("status = %+v, want ready provider", status)
	}
	capabilities := p.Capabilities()
	if len(capabilities.Modes) != 1 || capabilities.Modes[0].ID != VoiceModeApprovedClone {
		t.Fatalf("capabilities = %+v, want clone mode", capabilities)
	}

	capabilities.Modes[0].ID = VoiceModeStatic
	if p.Capabilities().Modes[0].ID != VoiceModeApprovedClone {
		t.Fatal("Capabilities() returned mutable provider-owned state")
	}
}

func TestProviderContractPreservesUnavailableCanceledAndUnsupportedStates(t *testing.T) {
	tests := []struct {
		name      string
		provider  *contractProvider
		wantError error
		wantText  string
	}{
		{
			name: "unavailable",
			provider: &contractProvider{
				available: false,
				status:    ProviderStatus{Provider: "fake", Available: false, ModelReady: false, Detail: "model missing"},
				request: func(context.Context, SynthesisRequest) (SynthesisResult, error) {
					return SynthesisResult{}, errors.New("provider unavailable")
				},
			},
			wantText: "provider unavailable",
		},
		{
			name: "canceled",
			provider: &contractProvider{
				available: true,
				status:    ProviderStatus{Provider: "fake", Available: true, ModelReady: true},
				request: func(ctx context.Context, _ SynthesisRequest) (SynthesisResult, error) {
					return SynthesisResult{}, ctx.Err()
				},
			},
			wantError: context.Canceled,
		},
		{
			name: "unsupported",
			provider: &contractProvider{
				available: true,
				status:    ProviderStatus{Provider: "fake", Available: true, ModelReady: true},
				request: func(context.Context, SynthesisRequest) (SynthesisResult, error) {
					return SynthesisResult{}, &UnsupportedFeatureError{Provider: "fake", Feature: "voice design"}
				},
			},
			wantError: ErrUnsupportedFeature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := tt.provider.SynthesizeRequest(ctx, SynthesisRequest{Text: "hello"})
			if err == nil {
				t.Fatal("SynthesizeRequest() error = nil")
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error = %v, want text %q", err, tt.wantText)
			}
			if tt.wantError != nil && !errors.Is(err, tt.wantError) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantError)
			}
		})
	}
}
