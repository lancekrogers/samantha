package brain

import (
	"fmt"
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
)

// ProviderSpec describes a brain provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

var providerSpecs = []ProviderSpec{
	{Name: "claude", Description: "Claude CLI"},
	{Name: "ollama", Description: "Local Ollama server"},
}

// Providers returns the list of implemented brain providers.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, len(providerSpecs))
	copy(out, providerSpecs)
	return out
}

// NewProvider constructs the configured brain provider.
func NewProvider(cfg *config.Config) (Provider, error) {
	switch normalizeProvider(cfg.BrainProvider) {
	case "", "claude":
		return New(cfg)
	case "ollama":
		return NewOllama(cfg)
	default:
		return nil, unsupportedProviderError("brain_provider", cfg.BrainProvider, providerSpecs)
	}
}

func normalizeProvider(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}

func unsupportedProviderError(key, configured string, specs []ProviderSpec) error {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported %s %q (implemented providers: %s)", key, configured, strings.Join(names, ", "))
}
