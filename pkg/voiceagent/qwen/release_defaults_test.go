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
	if gotURL, gotSHA := ResolveNativeDownload("", ""); gotURL != url || gotSHA != sha {
		t.Fatalf("ResolveNativeDownload() = %q/%q, want default pin %q/%q", gotURL, gotSHA, url, sha)
	}
	// The pin's digest must never attach to a different archive: a custom URL
	// with no explicit checksum resolves to an empty SHA (archive check
	// skipped; post-extract manifest verification still covers every file).
	if gotURL, gotSHA := ResolveNativeDownload("file:///tmp/custom.tar.gz", ""); gotURL != "file:///tmp/custom.tar.gz" || gotSHA != "" {
		t.Fatalf("ResolveNativeDownload(custom) = %q/%q, want custom URL with empty SHA", gotURL, gotSHA)
	}
	// An explicit checksum always wins.
	if _, gotSHA := ResolveNativeDownload("file:///tmp/custom.tar.gz", "abc123"); gotSHA != "abc123" {
		t.Fatalf("ResolveNativeDownload(custom, sha) sha = %q, want abc123", gotSHA)
	}
}

func TestResolveNativeURLConfigWins(t *testing.T) {
	if got := ResolveNativeURL("https://example.invalid/pkg.tar.gz"); got != "https://example.invalid/pkg.tar.gz" {
		t.Fatalf("got %q", got)
	}
}
