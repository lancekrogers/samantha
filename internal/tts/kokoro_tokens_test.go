package tts

import (
	"os"
	"path/filepath"
	"runtime"
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

	// Second call reuses the sidecar (content match, not just mtime).
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

func TestEnsureKokoroTokensAtomicNoPartialSidecar(t *testing.T) {
	dir := t.TempDir()
	stock := "n 7\n"
	if err := os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := ensureKokoroTokensWithSyllabicN(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No leftover temp files next to the sidecar.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tokens-samantha-") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != buildPatchedTokens(stock, 7) {
		t.Fatalf("sidecar content = %q", raw)
	}
}

func TestEnsureKokoroTokensFallsBackToUserCacheWhenModelsDirReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only models dir is unreliable on Windows")
	}

	modelsDir := t.TempDir()
	stock := "n 99\na 1\n"
	if err := os.WriteFile(filepath.Join(modelsDir, "tokens.txt"), []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make modelsDir non-writable so adjacent sidecar creation fails.
	if err := os.Chmod(modelsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(modelsDir, 0o755) })

	cacheRoot := t.TempDir()
	prevCache := userCacheDir
	userCacheDir = func() (string, error) { return cacheRoot, nil }
	t.Cleanup(func() { userCacheDir = prevCache })

	var warnings []string
	prevWarn := warnf
	warnf = func(format string, args ...any) {
		warnings = append(warnings, format)
	}
	t.Cleanup(func() { warnf = prevWarn })

	path, err := ensureKokoroTokensWithSyllabicN(modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(cacheRoot, "samantha", "kokoro-tokens")
	if !strings.HasPrefix(path, wantPrefix) {
		t.Fatalf("path = %q, want under cache %q", path, wantPrefix)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\u0329 99") {
		t.Fatalf("cache sidecar missing alias: %q", raw)
	}
	// modelsDir should not have a sidecar.
	if _, err := os.Stat(filepath.Join(modelsDir, sidecarName)); err == nil {
		t.Fatal("unexpected sidecar written into read-only models dir")
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings when cache write succeeded: %v", warnings)
	}
}

func TestEnsureKokoroTokensFallsBackToStockWhenUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only models dir is unreliable on Windows")
	}

	modelsDir := t.TempDir()
	stock := "n 3\n"
	src := filepath.Join(modelsDir, "tokens.txt")
	if err := os.WriteFile(src, []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(modelsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(modelsDir, 0o755) })

	// Cache also fails: return an unusable cache root path under a file.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevCache := userCacheDir
	userCacheDir = func() (string, error) { return blocker, nil }
	t.Cleanup(func() { userCacheDir = prevCache })

	var warnings []string
	prevWarn := warnf
	warnf = func(format string, args ...any) {
		warnings = append(warnings, format)
	}
	t.Cleanup(func() { warnf = prevWarn })

	path, err := ensureKokoroTokensWithSyllabicN(modelsDir)
	if err != nil {
		t.Fatalf("must not fail TTS init: %v", err)
	}
	if path != src {
		t.Fatalf("path = %q, want stock %q", path, src)
	}
	if len(warnings) == 0 {
		t.Fatal("expected stderr warning when falling back to stock tokens")
	}
}

func TestBuildPatchedTokens(t *testing.T) {
	got := buildPatchedTokens("n 1\n", 1)
	want := "n 1\n\u0329 1\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
