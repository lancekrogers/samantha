package tts

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// syllabicN is eSpeak's syllabic-n marker (U+0329). Kokoro's stock tokens.txt
// does not include it, so sherpa skips the phone and contractions like
// "wasn't" / stems like "button" lose audio. Alias it to an existing token id.
const syllabicN = '\u0329'

// sidecarName is written next to stock tokens (or under a user cache fallback).
const sidecarName = "tokens.samantha-syllabic-n.txt"

// userCacheDir resolves the OS user cache directory. Tests may override via
// setUserCacheDirForTest.
var userCacheDir = os.UserCacheDir

// warnf emits operator-visible warnings (stderr). Tests may override.
var warnf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "samantha: warning: "+format+"\n", args...)
}

// ensureKokoroTokensWithSyllabicN returns a tokens file path that maps U+0329
// to the same id as ASCII "n". When the stock tokens already define U+0329,
// the original path is returned unchanged.
//
// Otherwise a patched sidecar is written atomically (temp + rename). Write
// destinations, in order:
//  1. next to stock tokens in modelsDir
//  2. under the user cache (so read-only model packs still work)
//
// If every write fails, stock tokens are returned with a stderr warning so TTS
// still starts — contractions may clip until the pack is writable.
func ensureKokoroTokensWithSyllabicN(modelsDir string) (string, error) {
	src := filepath.Join(modelsDir, "tokens.txt")
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read kokoro tokens: %w", err)
	}
	text := string(raw)
	if tokenDefined(text, syllabicN) {
		return src, nil
	}

	nID, ok := tokenID(text, "n")
	if !ok {
		return "", fmt.Errorf("kokoro tokens.txt has no entry for n; cannot alias syllabic-n")
	}

	patched := buildPatchedTokens(text, nID)

	// Prefer modelsDir sidecar, then user-cache sidecar.
	candidates := []string{
		filepath.Join(modelsDir, sidecarName),
		cacheSidecarPath(modelsDir),
	}

	var lastWriteErr error
	for _, dst := range candidates {
		if dst == "" {
			continue
		}
		if data, err := os.ReadFile(dst); err == nil && string(data) == patched {
			return dst, nil
		}
		path, err := writeTokensAtomic(dst, []byte(patched))
		if err == nil {
			return path, nil
		}
		lastWriteErr = err
	}

	// Keep TTS usable on read-only installs; contractions may still clip.
	if lastWriteErr != nil {
		warnf("could not write Kokoro syllabic-n token alias (%v); using stock tokens — contractions like \"wasn't\" may clip", lastWriteErr)
	}
	return src, nil
}

// buildPatchedTokens appends a U+0329 → nID line to stock tokens text.
func buildPatchedTokens(stockText string, nID int) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(stockText, "\n"))
	b.WriteByte('\n')
	// Format matches stock lines: "<symbol> <id>\n"
	b.WriteRune(syllabicN)
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(nID))
	b.WriteByte('\n')
	return b.String()
}

// writeTokensAtomic writes content to dst via a same-directory temp file and
// rename so a crash mid-write cannot leave a half sidecar that later looks valid.
func writeTokensAtomic(dst string, content []byte) (string, error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create tokens dir %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, ".tokens-samantha-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp tokens file: %w", err)
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write temp tokens file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp tokens file: %w", err)
	}
	// Best-effort mode bits (Windows may ignore).
	_ = os.Chmod(tmp, 0o644)

	// Rename replaces on Unix; on Windows remove dest first if present.
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(dst)
		if err2 := os.Rename(tmp, dst); err2 != nil {
			return "", fmt.Errorf("rename patched tokens into place: %w", err2)
		}
	}
	cleanup = false
	return dst, nil
}

// cacheSidecarPath is a writable fallback when modelsDir is read-only
// (system-installed packs, etc.).
func cacheSidecarPath(modelsDir string) string {
	base, err := userCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return ""
	}
	key := filepath.Base(filepath.Clean(modelsDir))
	if key == "" || key == "." || key == string(filepath.Separator) {
		key = "default"
	}
	return filepath.Join(base, "samantha", "kokoro-tokens", key, sidecarName)
}

func tokenDefined(tokensText string, r rune) bool {
	sym := string(r)
	sc := bufio.NewScanner(strings.NewReader(tokensText))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "sym id" — symbol may be multi-byte; first field is the symbol.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == sym {
			return true
		}
		// Single-rune compare for combining marks that Fields may still keep.
		if fr, _ := utf8.DecodeRuneInString(fields[0]); fr == r {
			return true
		}
	}
	return false
}

func tokenID(tokensText, sym string) (int, bool) {
	sc := bufio.NewScanner(strings.NewReader(tokensText))
	for sc.Scan() {
		fields := strings.Fields(strings.TrimSpace(sc.Text()))
		if len(fields) < 2 {
			continue
		}
		if fields[0] != sym {
			continue
		}
		id, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, false
		}
		return id, true
	}
	return 0, false
}
