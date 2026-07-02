package extractors

import (
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/render"
)

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
	// Prefer the main content region when present: this drops social/share,
	// comment, and related-links boilerplate that lives outside <article>/<main>.
	s = mainRegion(s)

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

var (
	articleRE = regexp.MustCompile(`(?is)<article\b[^>]*>(.*)</article>`)
	mainRE    = regexp.MustCompile(`(?is)<main\b[^>]*>(.*)</main>`)
)

// mainRegion returns the content of the first <article> (or, failing that,
// <main>) region when present; otherwise it returns s unchanged. Using a greedy
// match keeps nested content. This is a simple readability heuristic that
// excludes page chrome outside the main content.
func mainRegion(s string) string {
	if m := articleRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := mainRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// boilerplateRE holds each boilerplate tag's block/bare regex pair, compiled
// once at init — RE2 has no backreferences so every tag needs its own pair, and
// ExtractHTML runs once per chapter of a book.
var boilerplateRE = func() map[string][2]*regexp.Regexp {
	m := make(map[string][2]*regexp.Regexp, len(boilerplate))
	for _, tag := range boilerplate {
		m[tag] = [2]*regexp.Regexp{
			regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*?</` + tag + `\s*>`),
			regexp.MustCompile(`(?is)<` + tag + `\b[^>]*/?>`),
		}
	}
	return m
}()

// dropBlock removes every <tag ...>...</tag> block (case-insensitive), plus any
// self-closing or unmatched opener.
func dropBlock(s, tag string) string {
	res := boilerplateRE[tag]
	s = res[0].ReplaceAllString(s, " ")
	return res[1].ReplaceAllString(s, " ")
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
