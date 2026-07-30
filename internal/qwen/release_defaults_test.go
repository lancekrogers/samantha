package qwen

import (
	"runtime"
	"strings"
	"testing"
)

func TestDefaultNativeReleaseDarwinArm64(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("platform defaults are per-OS")
	}
	url, sha := DefaultNativeRelease()
	if !strings.Contains(url, "qwen3-tts-native-darwin-arm64.tar.gz") {
		t.Fatalf("url = %q", url)
	}
	if !strings.HasPrefix(url, "https://github.com/Obedience-Corp/qwen3-tts-native/releases/download/") {
		t.Fatalf("url should be GitHub release asset: %q", url)
	}
	if len(sha) != 64 {
		t.Fatalf("sha length = %d, want 64 hex", len(sha))
	}
	if got := ResolveNativeURL(""); got != url {
		t.Fatalf("ResolveNativeURL() = %q, want default %q", got, url)
	}
	if got := ResolveNativeSHA256(""); got != sha {
		t.Fatalf("ResolveNativeSHA256() = %q, want default", got)
	}
}

func TestResolveNativeURLConfigWins(t *testing.T) {
	if got := ResolveNativeURL("https://example.invalid/pkg.tar.gz"); got != "https://example.invalid/pkg.tar.gz" {
		t.Fatalf("got %q", got)
	}
}
