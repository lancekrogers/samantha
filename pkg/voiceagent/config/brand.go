package config

import (
	"path/filepath"
)

// AppSlug is the filesystem branding slug for install-scoped paths
// (config, model cache, fixture cache, etc.). Change this single value
// when rebranding the product; persona/agent names are separate.
//
// Examples:
//
//	~/.obey/agents/voice/<AppSlug>/
//	~/.cache/<AppSlug>/models/
//
// Note: models/fixtures use ~/.cache (not OS UserCacheDir) so Linux/macOS
// share the same documented layout and migrations stay a simple rename.
const AppSlug = "festival-voice"

// DefaultConfigDir returns the default on-disk config root for this install.
func DefaultConfigDir() string {
	return filepath.Join(homeDir(), ".obey", "agents", "voice", AppSlug)
}

// DefaultCacheDir returns the default cache root (~/.cache/<AppSlug>).
func DefaultCacheDir() string {
	return filepath.Join(homeDir(), ".cache", AppSlug)
}

// DefaultModelsDir returns the default model download/cache directory.
func DefaultModelsDir() string {
	return filepath.Join(DefaultCacheDir(), "models")
}
