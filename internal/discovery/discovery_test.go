package discovery

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestDiscoverProvidersIncludesAllBackends(t *testing.T) {
	// Unreachable host so the ollama probe fails fast instead of waiting on the network.
	cfg := &config.Config{OllamaHost: "http://127.0.0.1:1"}

	found := map[string]bool{}
	for _, p := range DiscoverProviders(cfg) {
		found[p.Name] = true
	}

	for _, name := range []string{"claude", "grok", "ollama"} {
		if !found[name] {
			t.Errorf("DiscoverProviders() missing %q provider; got %v", name, found)
		}
	}
}
