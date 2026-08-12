//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// Conversation path may attach Kokoro as fallback when primary is qwen3-tts.
// Preview/batch/narrate must call tts.NewProvider directly (fail closed) so a
// missing or broken Qwen never silently produces a Kokoro voice mid-book.
func TestNewTTSProviderSetConversationFallback(t *testing.T) {
	// Use a fake absolute "binary" that is the test executable so LookPath works
	// for the external CLI path without starting a real model.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	modelDir := t.TempDir()
	// Create a non-qwen3-tts-worker name so we take the CLI adapter path.
	// Factory needs a resolvable binary for qwen3-tts.
	bin := filepath.Join(t.TempDir(), "qwen3-tts-cli")
	if err := os.Symlink(exe, bin); err != nil {
		// Windows or symlink failure: copy not needed if we skip
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		TTSProvider:         "qwen3-tts",
		TTSFallbackProvider: "kokoro",
		QwenTTSBinary:       bin,
		QwenTTSModel:        modelDir,
	}
	set, err := newTTSProviderSet(cfg)
	if err != nil {
		// Kokoro may also fail if models missing — primary must still construct for CLI path.
		// If primary fails, that's a different bug.
		t.Fatalf("newTTSProviderSet: %v", err)
	}
	defer set.Close()
	if set.Primary == nil {
		t.Fatal("primary required")
	}
	// Fallback may be nil if Kokoro assets are not installed in this environment;
	// the warning must then be set (fail soft on fallback construction only).
	if set.Fallback == nil && set.FallbackWarning == nil {
		t.Fatal("expected either Kokoro fallback or a FallbackWarning when assets missing")
	}
	if set.FallbackWarning != nil && !strings.Contains(set.FallbackWarning.Error(), "fallback") {
		t.Fatalf("unexpected warning: %v", set.FallbackWarning)
	}
}

func TestNewTTSProviderSetNoFallbackWhenPrimaryIsKokoro(t *testing.T) {
	// When primary is already Kokoro, do not double-attach a fallback set.
	cfg := &config.Config{
		TTSProvider:         "kokoro",
		TTSFallbackProvider: "kokoro",
	}
	// Construction may fail without Kokoro models — only assert policy when it succeeds.
	set, err := newTTSProviderSet(cfg)
	if err != nil {
		t.Skipf("kokoro not installable in this environment: %v", err)
	}
	defer set.Close()
	if set.Fallback != nil {
		t.Fatal("fallback must be nil when primary is already kokoro")
	}
}
