package render

import "strings"

// DocumentSection is one structural section of an extracted document (typically
// introduced by a heading). Children allow nesting, though the first extractors
// emit a flat list.
type DocumentSection struct {
	ID       string
	Title    string
	Level    int
	Text     string
	Children []DocumentSection
}

// Document is a structure-aware extraction of an input document, produced by a
// format extractor and consumed by the render runtime.
type Document struct {
	ID       string
	Title    string
	Author   string
	Source   string
	Format   Format
	Sections []DocumentSection
}

// Narration returns the full readable text of the document: each section's
// title (if any) followed by its body, sections separated by blank lines. This
// is the single-file render input.
func (d Document) Narration() string {
	var b strings.Builder
	for _, s := range d.Sections {
		seg := s.narration()
		if seg == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(seg)
	}
	return b.String()
}

// Units converts the document's top-level sections into ordered render units:
// one unit per section, narrated the same way Narration() reads it (title then
// body). Section IDs, titles, and text pass through unchanged, so unit
// identity is as stable as the extractor's section IDs. A section with no
// narratable text converts to an empty-text unit, which the render path
// records as skipped — identical to an empty chapter.
func (d Document) Units() []RenderUnit {
	units := make([]RenderUnit, 0, len(d.Sections))
	for _, s := range d.Sections {
		units = append(units, RenderUnit{ID: s.ID, Title: s.Title, Text: s.narration(), SourceRef: d.Source})
	}
	return units
}

func (s DocumentSection) narration() string {
	var parts []string
	if t := strings.TrimSpace(s.Title); t != "" {
		parts = append(parts, t)
	}
	if t := strings.TrimSpace(s.Text); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n\n")
}
