package stt

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/config"
)

// ProviderSpec describes an STT provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

var providerSpecs = []ProviderSpec{
	{Name: "sherpa", Description: "Local sherpa-onnx Whisper"},
}

// Providers returns the list of implemented STT providers.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, len(providerSpecs))
	copy(out, providerSpecs)
	return out
}

// NewProvider constructs the configured STT provider and its cleanup hook.
func NewProvider(cfg *config.Config, capture *audio.Capture, vad *audio.VAD) (Provider, func(), error) {
	switch strings.TrimSpace(strings.ToLower(cfg.STTProvider)) {
	case "", "sherpa":
		if capture == nil {
			return nil, nil, fmt.Errorf("sherpa STT requires audio capture")
		}
		if vad == nil {
			return nil, nil, fmt.Errorf("sherpa STT requires VAD; set vad_enabled=true or choose a different stt_provider")
		}

		provider, err := NewSherpaSTT(cfg, capture, vad)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	default:
		return nil, nil, unsupportedProviderError(cfg.STTProvider)
	}
}

func unsupportedProviderError(configured string) error {
	names := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported stt_provider %q (implemented providers: %s)", configured, strings.Join(names, ", "))
}
