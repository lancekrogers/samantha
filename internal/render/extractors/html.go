package extractors

import (
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/render"
)

// DocumentExtractor is the boundary for structure-aware extraction. HTML/URL
// share one extractor so a future readability-style implementation can replace
// it without changing render orchestration.
type DocumentExtractor interface {
	Extract(source string, data []byte) (render.Document, error)
}

// HTML handling (conservative first pass, no parser dependency): comments and
// script/style/nav/header/footer/aside/form blocks are removed, <title> becomes
// the document title, headings (h1-6) start sections, p/li/blockquote/br mark
// paragraph boundaries, <img alt> renders the alt text, all other tags are
// stripped, and HTML entities are decoded.

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTitleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	imgAltRE    = regexp.MustCompile(`(?is)\balt\s*=\s*"([^"]*)"|\balt\s*=\s*'([^']*)'`)
	tagNameRE   = regexp.MustCompile(`^</?\s*([a-zA-Z0-9]+)`)
	// Boilerplate container tags whose entire contents are dropped.
	boilerplate = []string{"script", "style", "nav", "header", "footer", "aside", "form", "noscript", "head"}
)

// ExtractHTML parses HTML bytes into a render.Document.
func ExtractHTML(source string, data []byte) (render.Document, error) {
	raw := string(data)

	title := ""
	if m := htmlTitleRE.FindStringSubmatch(raw); m != nil {
		title = cleanText(decodeEntities(m[1]))
	}

	s := htmlComment.ReplaceAllString(raw, " ")
	for _, tag := range boilerplate {
		s = dropBlock(s, tag)
	}

	doc := render.Document{Source: source, Format: render.FormatHTML, Title: title}
	var sections []render.DocumentSection
	curTitle := ""
	curLevel := 0
	started := false
	var paragraphs []string
	var para strings.Builder
	var heading strings.Builder
	inHeading := false
	headingLevel := 0

	flushPara := func() {
		if t := strings.TrimSpace(para.String()); t != "" {
			paragraphs = append(paragraphs, t)
		}
		para.Reset()
	}
	closeSection := func() {
		flushPara()
		text := strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
		if started || text != "" || curTitle != "" {
			sections = append(sections, render.DocumentSection{
				ID:    sectionID(len(sections)+1, curTitle),
				Title: curTitle,
				Level: curLevel,
				Text:  text,
			})
		}
		paragraphs = nil
		curTitle = ""
		curLevel = 0
		started = false
	}
	target := func() *strings.Builder {
		if inHeading {
			return &heading
		}
		return &para
	}
	writeText := func(run string) {
		run = cleanText(decodeEntities(run))
		if run == "" {
			return
		}
		t := target()
		if t.Len() > 0 && !strings.HasSuffix(t.String(), " ") {
			t.WriteByte(' ')
		}
		t.WriteString(run)
	}

	for i := 0; i < len(s); {
		if s[i] == '<' {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				break
			}
			tag := s[i : i+end+1]
			i += end + 1

			name, closing := tagInfo(tag)
			switch {
			case isHeadingTag(name) && !closing:
				closeSection()
				inHeading = true
				headingLevel = int(name[1] - '0')
				heading.Reset()
			case isHeadingTag(name) && closing:
				curTitle = cleanText(heading.String())
				curLevel = headingLevel
				inHeading = false
				started = true
			case name == "img":
				if alt := imgAlt(tag); alt != "" {
					writeText(alt)
				}
			case closing && (name == "p" || name == "li" || name == "blockquote" || name == "div"):
				flushPara()
			case name == "br":
				flushPara()
			}
			continue
		}

		next := strings.IndexByte(s[i:], '<')
		if next < 0 {
			writeText(s[i:])
			break
		}
		writeText(s[i : i+next])
		i += next
	}
	closeSection()

	doc.Sections = sections
	if doc.Title == "" {
		for _, sec := range sections {
			if sec.Title != "" {
				doc.Title = sec.Title
				break
			}
		}
	}
	return doc, nil
}

// dropBlock removes every <tag ...>...</tag> block (case-insensitive). RE2 has
// no backreferences, so this is built per tag.
func dropBlock(s, tag string) string {
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*?</` + tag + `\s*>`)
	s = re.ReplaceAllString(s, " ")
	// Self-closing or unmatched opener: also strip a bare leading tag.
	bare := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*/?>`)
	return bare.ReplaceAllString(s, " ")
}

func tagInfo(tag string) (name string, closing bool) {
	closing = strings.HasPrefix(tag, "</")
	if m := tagNameRE.FindStringSubmatch(tag); m != nil {
		name = strings.ToLower(m[1])
	}
	return name, closing
}

func isHeadingTag(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

func imgAlt(tag string) string {
	if m := imgAltRE.FindStringSubmatch(tag); m != nil {
		if m[1] != "" {
			return cleanText(decodeEntities(m[1]))
		}
		return cleanText(decodeEntities(m[2]))
	}
	return ""
}

var entityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
	"&#39;", "'", "&apos;", "'", "&nbsp;", " ", "&mdash;", "—",
	"&ndash;", "–", "&hellip;", "…", "&rsquo;", "'", "&lsquo;", "'",
	"&ldquo;", `"`, "&rdquo;", `"`,
)

func decodeEntities(s string) string { return entityReplacer.Replace(s) }

func cleanText(s string) string { return strings.Join(strings.Fields(s), " ") }
