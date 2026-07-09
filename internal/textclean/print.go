package textclean

import (
	"regexp"
	"strings"
)

var (
	pageNumberLine = regexp.MustCompile(`(?m)^\s*\d{1,4}\s*$`)
	hyphenBreak    = regexp.MustCompile(`([A-Za-z])-\n([a-z])`)
)

// CleanPrintArtifacts applies conservative PDF print cleanup: de-hyphenates
// line breaks, drops standalone page-number lines, collapses excess blank
// lines, and joins soft-wrapped paragraph lines.
func CleanPrintArtifacts(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = hyphenBreak.ReplaceAllString(s, "$1$2")
	s = pageNumberLine.ReplaceAllString(s, "")

	// Collapse 3+ newlines to 2 (paragraph break).
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}

	// Within a paragraph (single newlines), join wrapped lines with a space.
	paras := strings.Split(s, "\n\n")
	for i, p := range paras {
		lines := strings.Split(p, "\n")
		var b strings.Builder
		for j, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(line)
			_ = j
		}
		paras[i] = b.String()
	}
	return strings.TrimSpace(strings.Join(paras, "\n\n"))
}
