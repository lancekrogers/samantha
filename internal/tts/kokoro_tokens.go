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

// ensureKokoroTokensWithSyllabicN returns a tokens file path that maps U+0329
// to the same id as ASCII "n". When the stock tokens already define U+0329,
// the original path is returned unchanged. Otherwise a sidecar file is written
// next to tokens.txt (tokens.samantha-syllabic-n.txt).
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

	dst := filepath.Join(modelsDir, "tokens.samantha-syllabic-n.txt")
	// Rebuild when missing or stale relative to stock tokens.
	if fi, err := os.Stat(dst); err == nil {
		if srcInfo, err2 := os.Stat(src); err2 == nil && !fi.ModTime().Before(srcInfo.ModTime()) {
			if patched, err := os.ReadFile(dst); err == nil && tokenDefined(string(patched), syllabicN) {
				return dst, nil
			}
		}
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(text, "\n"))
	b.WriteByte('\n')
	// Format matches stock lines: "<symbol> <id>\n"
	b.WriteRune(syllabicN)
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(nID))
	b.WriteByte('\n')

	if err := os.WriteFile(dst, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write patched kokoro tokens: %w", err)
	}
	return dst, nil
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
