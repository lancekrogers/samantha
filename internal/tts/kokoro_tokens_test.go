package tts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureKokoroTokensWithSyllabicN(t *testing.T) {
	dir := t.TempDir()
	stock := "a 1\nn 56\nᵊ 42\n"
	if err := os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := ensureKokoroTokensWithSyllabicN(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(dir, "tokens.txt") {
		t.Fatal("expected sidecar tokens path when stock lacks syllabic-n")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\u0329 56") {
		t.Fatalf("patched tokens missing U+0329→n alias:\n%s", raw)
	}

	// Second call reuses the sidecar.
	path2, err := ensureKokoroTokensWithSyllabicN(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path2 != path {
		t.Fatalf("path drifted: %q vs %q", path2, path)
	}
}

func TestEnsureKokoroTokensAlreadyHasSyllabicN(t *testing.T) {
	dir := t.TempDir()
	stock := "n 56\n\u0329 56\n"
	src := filepath.Join(dir, "tokens.txt")
	if err := os.WriteFile(src, []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := ensureKokoroTokensWithSyllabicN(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != src {
		t.Fatalf("expected stock tokens, got %q", path)
	}
}
