package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAppSlugIsFestivalVoice(t *testing.T) {
	if AppSlug != "festival-voice" {
		t.Fatalf("AppSlug = %q, want festival-voice", AppSlug)
	}
}

func TestDefaultPathsUseAppSlug(t *testing.T) {
	cfg := DefaultConfigDir()
	if !strings.HasSuffix(cfg, filepath.Join("agents", "voice", AppSlug)) {
		t.Fatalf("DefaultConfigDir() = %q, want .../agents/voice/%s", cfg, AppSlug)
	}
	cache := DefaultCacheDir()
	if filepath.Base(cache) != AppSlug {
		t.Fatalf("DefaultCacheDir() = %q, want base %q", cache, AppSlug)
	}
	models := DefaultModelsDir()
	if !strings.HasSuffix(models, filepath.Join(AppSlug, "models")) {
		t.Fatalf("DefaultModelsDir() = %q, want .../%s/models", models, AppSlug)
	}
}
