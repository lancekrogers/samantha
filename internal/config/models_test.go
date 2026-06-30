package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeKokoroLexiconsRemovesUnsupportedMarks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lexicon-us-en.txt")
	if err := os.WriteFile(path, []byte("button b\u0329t\u0301n\n"), 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := sanitizeKokoroLexicons(dir); err != nil {
		t.Fatalf("sanitizeKokoroLexicons() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "button btn\n"; got != want {
		t.Fatalf("sanitized lexicon = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("sanitized lexicon mode = %v, want %v", got, want)
	}
}

func TestSanitizeKokoroLexiconsAllowsMissingLexicons(t *testing.T) {
	if err := sanitizeKokoroLexicons(t.TempDir()); err != nil {
		t.Fatalf("sanitizeKokoroLexicons() error = %v", err)
	}
}
