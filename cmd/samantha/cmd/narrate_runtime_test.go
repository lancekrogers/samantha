//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPromptProfileIsKindScoped(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "style"), 0o755); err != nil {
		t.Fatal(err)
	}
	stylePath := filepath.Join(dir, "style", "audiobook.md")
	if err := os.WriteFile(stylePath, []byte("Read warmly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	system, style, pron, err := loadPromptProfile(dir, "audiobook")
	if err != nil {
		t.Fatal(err)
	}
	if system != "" || pron != "" {
		t.Fatalf("style-only profile leaked into other kinds: system=%q pron=%q", system, pron)
	}
	if style != stylePath {
		t.Fatalf("style = %q, want %q", style, stylePath)
	}
}

func TestLoadPromptProfileBareNameIsSystemOnly(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "audiobook.md")
	if err := os.WriteFile(bare, []byte("System prompt.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	system, style, pron, err := loadPromptProfile(dir, "audiobook")
	if err != nil {
		t.Fatal(err)
	}
	if system != bare {
		t.Fatalf("system = %q, want %q", system, bare)
	}
	if style != "" || pron != "" {
		t.Fatalf("bare name.md must not satisfy style/pronunciation: style=%q pron=%q", style, pron)
	}
}
