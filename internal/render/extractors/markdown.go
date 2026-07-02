// Package extractors turns input documents (Markdown, HTML, URL articles, EPUB)
// into structure-aware render.Documents. Extractors are cgo-free and testable
// without network access.
package extractors

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/render"
)

// Markdown handling notes (conservative first pass, no parser dependency):
//   - YAML front matter (--- ... ---) at the top is dropped; a "title:" field
//     becomes the document title.
//   - ATX headings (#..######) start sections; the text becomes the title.
//   - Fenced code blocks (``` / ~~~) are skipped (the design's default
//     code-blocks=skip policy); inline `code` keeps its text.
//   - Links [text](url) and images ![alt](url) render as their text/alt.
//   - Emphasis (*, _, **) markers, list bullets, and blockquote markers are
//     stripped; horizontal rules are dropped.

var (
	atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdLink     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	listMarker = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
)

// isHorizontalRule reports whether trimmed is a thematic break (3+ of the same
// -, *, or _, optionally spaced). Go's RE2 lacks backreferences, so this is a
// small helper rather than a regex.
func isHorizontalRule(trimmed string) bool {
	var marker byte
	count := 0
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c != '-' && c != '*' && c != '_' {
			return false
		}
		if marker == 0 {
			marker = c
		} else if c != marker {
			return false
		}
		count++
	}
	return count >= 3
}

// ExtractMarkdown parses Markdown bytes into a render.Document. source labels
// the origin (file path) for the manifest.
func ExtractMarkdown(source string, data []byte) (render.Document, error) {
	body, frontTitle := stripFrontMatter(string(data))
	lines := strings.Split(body, "\n")

	doc := render.Document{Source: source, Format: render.FormatMarkdown, Title: frontTitle}

	var sections []render.DocumentSection
	var curTitle string
	curLevel := 0
	started := false
	var paragraphs []string
	var para strings.Builder
	inCode := false

	flushPara := func() {
		if t := strings.TrimSpace(para.String()); t != "" {
			paragraphs = append(paragraphs, t)
		}
		para.Reset()
	}
	closeSection := func() {
		flushPara()
		text := strings.Join(paragraphs, "\n\n")
		if started || strings.TrimSpace(text) != "" || curTitle != "" {
			sections = append(sections, render.DocumentSection{
				ID:    sectionID(len(sections)+1, curTitle),
				Title: curTitle,
				Level: curLevel,
				Text:  strings.TrimSpace(text),
			})
		}
		paragraphs = nil
		curTitle = ""
		curLevel = 0
		started = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushPara()
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if trimmed == "" {
			flushPara()
			continue
		}
		if isHorizontalRule(trimmed) {
			continue
		}

		if m := atxHeading.FindStringSubmatch(trimmed); m != nil {
			closeSection()
			curLevel = len(m[1])
			curTitle = cleanInline(m[2])
			started = true
			continue
		}

		// Body line: strip list/blockquote markers and inline formatting.
		text := listMarker.ReplaceAllString(trimmed, "")
		text = strings.TrimPrefix(text, ">")
		text = cleanInline(strings.TrimSpace(text))
		if text == "" {
			continue
		}
		started = true
		if para.Len() > 0 {
			para.WriteByte(' ')
		}
		para.WriteString(text)
	}
	closeSection()

	doc.Sections = sections
	if doc.Title == "" {
		for _, s := range sections {
			if s.Title != "" {
				doc.Title = s.Title
				break
			}
		}
	}
	return doc, nil
}

// stripFrontMatter removes a leading YAML front matter block and returns the
// remaining body plus the "title:" value when present.
func stripFrontMatter(s string) (body, title string) {
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return s, ""
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return s, "" // no closing fence; treat as body
	}
	front := rest[:end]
	for _, line := range strings.Split(front, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "title:"); ok {
			title = strings.Trim(strings.TrimSpace(t), `"'`)
		}
	}
	body = rest[end+len("\n---"):]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return body, title
}

// cleanInline strips inline Markdown formatting, leaving readable text.
func cleanInline(s string) string {
	s = mdLink.ReplaceAllString(s, "$1") // [text](url)/![alt](url) -> text/alt
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	// Strip stray single emphasis markers (* and _) used around words.
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "_", "")
	return strings.TrimSpace(s)
}

// sectionID returns a stable, human-readable section id.
func sectionID(index int, title string) string {
	slug := render.Slugify(title)
	if slug == "" {
		return fmt.Sprintf("sec-%03d", index)
	}
	return fmt.Sprintf("sec-%03d-%s", index, slug)
}
