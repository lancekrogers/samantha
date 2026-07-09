package render

import (
	"context"
	"reflect"
	"testing"
)

// TestDocumentUnitsFallbackWhenNoSections locks the heading-less path: a
// structured document with no sections still yields one unit so --out-dir works.
func TestDocumentUnitsFallbackWhenNoSections(t *testing.T) {
	doc := Document{Title: "Notes", Source: "notes.md", Format: FormatMarkdown}
	want := []RenderUnit{{
		ID: "sec-001-notes", Title: "Notes", Text: "Notes", SourceRef: "notes.md",
	}}
	if got := doc.Units(); !reflect.DeepEqual(got, want) {
		t.Errorf("Units() = %+v, want %+v", got, want)
	}
}

// TestDocumentUnitsStableIDsTitlesText locks the section-to-unit conversion:
// section IDs and titles pass through, unit text is the section narration
// (title then body), and every unit carries the document source.
func TestDocumentUnitsStableIDsTitlesText(t *testing.T) {
	doc := Document{
		Title:  "Guide",
		Source: "guide.md",
		Format: FormatMarkdown,
		Sections: []DocumentSection{
			{ID: "sec-001-intro", Title: "Intro", Level: 1, Text: "Welcome to the guide."},
			{ID: "sec-002", Level: 1, Text: "An untitled preamble."},
			{ID: "sec-003-images", Title: "Images", Level: 2, Text: "   "},
		},
	}

	want := []RenderUnit{
		{ID: "sec-001-intro", Title: "Intro", Text: "Intro\n\nWelcome to the guide.", SourceRef: "guide.md"},
		{ID: "sec-002", Text: "An untitled preamble.", SourceRef: "guide.md"},
		{ID: "sec-003-images", Title: "Images", Text: "Images", SourceRef: "guide.md"},
	}
	if got := doc.Units(); !reflect.DeepEqual(got, want) {
		t.Errorf("Units() = %+v, want %+v", got, want)
	}
}

// TestDocumentUnitsEmptySectionSkipsLikeEmptyChapter proves a section with no
// narratable text rendered through the unit path behaves exactly like an empty
// chapter: no WAV written, skipped with no output or resume key, and a manifest
// identical to RenderChapters over the same texts.
func TestDocumentUnitsEmptySectionSkipsLikeEmptyChapter(t *testing.T) {
	doc := Document{
		Source: "guide.md",
		Sections: []DocumentSection{
			{ID: "sec-001", Text: "   "}, // no narratable text
			{ID: "sec-002", Title: "Body", Text: "Real content here."},
		},
	}
	opts := Options{OutDir: t.TempDir(), Format: FormatEPUB, Title: "Book"}

	var unitWrites []string
	um, err := RenderUnits(context.Background(), opts, doc.Units(), &fakeSynth{rate: 24000}, recordingWriter(&unitWrites))
	if err != nil {
		t.Fatalf("RenderUnits() error = %v", err)
	}
	if len(unitWrites) != 1 {
		t.Fatalf("wrote %d WAV(s), want 1 (empty section must not be written): %v", len(unitWrites), unitWrites)
	}
	empty := um.Segments[0]
	if empty.Status != StatusSkipped || empty.Output != "" || empty.ResumeKey != "" {
		t.Errorf("empty section segment = %+v, want skipped with no output/resume key", empty)
	}

	chapters := make([]RenderChapter, 0, len(doc.Sections))
	for _, s := range doc.Sections {
		chapters = append(chapters, RenderChapter{ID: s.ID, Title: s.Title, Text: s.narration()})
	}
	cm, err := RenderChapters(context.Background(), opts, chapters, &fakeSynth{rate: 24000}, recordingWriter(new([]string)))
	if err != nil {
		t.Fatalf("RenderChapters() error = %v", err)
	}
	if !reflect.DeepEqual(um, cm) {
		t.Errorf("unit manifest diverged from chapter manifest.\n got: %+v\nwant: %+v", um, cm)
	}
}

// TestStructuredSectionFilenamesStable proves Markdown and HTML headings map to
// deterministic section unit filenames under --out-dir.
func TestStructuredSectionFilenamesStable(t *testing.T) {
	md := Document{
		Source: "guide.md",
		Sections: []DocumentSection{
			{ID: "sec-001-intro", Title: "Intro", Text: "Hello."},
			{ID: "sec-002-body", Title: "Body", Text: "More."},
		},
	}
	html := Document{
		Source: "page.html",
		Sections: []DocumentSection{
			{ID: "sec-001-overview", Title: "Overview", Text: "Start."},
		},
	}
	for _, tc := range []struct {
		name string
		doc  Document
		want []string
	}{
		{"markdown", md, []string{"001-intro.wav", "002-body.wav"}},
		{"html", html, []string{"001-overview.wav"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units := tc.doc.Units()
			if len(units) != len(tc.want) {
				t.Fatalf("units = %d, want %d", len(units), len(tc.want))
			}
			for i, u := range units {
				if got := unitFilename(i+1, u); got != tc.want[i] {
					t.Errorf("unitFilename(%d) = %q, want %q", i+1, got, tc.want[i])
				}
			}
		})
	}
}

// TestRenderUnitsFromDocumentMarkdownHTMLResume covers sectioned Markdown/HTML
// units and resume: unchanged sections skip, changed text re-renders.
func TestRenderUnitsFromDocumentMarkdownHTMLResume(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatMarkdown, Title: "Guide", Resume: true}
	doc := Document{
		Source: "guide.md",
		Sections: []DocumentSection{
			{ID: "sec-001-intro", Title: "Intro", Text: "Hello."},
			{ID: "sec-002-body", Title: "Body", Text: "World."},
		},
	}
	units := doc.Units()
	var writes []string
	m1, err := RenderUnits(context.Background(), opts, units, &fakeSynth{rate: 24000}, recordingWriter(&writes))
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if err := WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if len(writes) != 2 {
		t.Fatalf("first render wrote %d, want 2", len(writes))
	}

	// Resume unchanged: no new writes.
	writes = nil
	m2, err := RenderUnits(context.Background(), opts, units, &fakeSynth{rate: 24000}, recordingWriter(&writes))
	if err != nil {
		t.Fatalf("resume unchanged: %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("resume wrote %v, want none", writes)
	}
	for _, s := range m2.Segments {
		if s.Status != StatusSkipped {
			t.Errorf("segment %s status = %s, want skipped", s.ID, s.Status)
		}
	}

	// Change second section: only that unit re-renders.
	doc.Sections[1].Text = "Changed."
	writes = nil
	_, err = RenderUnits(context.Background(), opts, doc.Units(), &fakeSynth{rate: 24000}, recordingWriter(&writes))
	if err != nil {
		t.Fatalf("resume changed: %v", err)
	}
	if len(writes) != 1 {
		t.Fatalf("changed resume wrote %d, want 1: %v", len(writes), writes)
	}
}
