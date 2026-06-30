package discovery

import (
	"context"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/config"
)

// ProviderInfo describes an available brain provider.
type ProviderInfo struct {
	Name      string
	Available bool
	Models    []string
}

// DiscoverProviders probes the system for available brain providers.
func DiscoverProviders(cfg *config.Config) []ProviderInfo {
	return []ProviderInfo{
		discoverClaude(),
		discoverGrok(),
		discoverOllama(cfg),
	}
}

func discoverClaude() ProviderInfo {
	_, err := exec.LookPath("claude")
	return ProviderInfo{
		Name:      "claude",
		Available: err == nil,
		Models:    []string{"default"},
	}
}

func discoverGrok() ProviderInfo {
	info := ProviderInfo{Name: "grok"}

	binPath, err := grok.LocateBinary()
	if err != nil {
		return info
	}
	info.Available = true

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	models, err := grok.NewClient(binPath).Models(ctx)
	if err != nil {
		info.Models = []string{"default"}
		return info
	}
	for _, mod := range models {
		info.Models = append(info.Models, mod.ID)
	}
	if len(info.Models) == 0 {
		info.Models = []string{"default"}
	}

	return info
}

func discoverOllama(cfg *config.Config) ProviderInfo {
	info := ProviderInfo{Name: "ollama"}

	base, err := url.Parse(cfg.OllamaHost)
	if err != nil {
		return info
	}

	client := api.NewClient(base, http.DefaultClient)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.List(ctx)
	if err != nil {
		return info
	}

	info.Available = true
	for _, m := range resp.Models {
		info.Models = append(info.Models, strings.TrimSuffix(m.Name, ":latest"))
	}

	return info
}
