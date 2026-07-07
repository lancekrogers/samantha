package render

import (
	"context"
	"reflect"
	"testing"
)

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
