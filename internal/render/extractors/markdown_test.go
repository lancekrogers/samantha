package extractors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
)

func extractFixture(t *testing.T) render.Document {
	t.Helper()
	path := filepath.Join("..", "..", "..", "tests", "fixtures", "documents", "article.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := ExtractMarkdown(path, data)
	if err != nil {
		t.Fatalf("ExtractMarkdown() error = %v", err)
	}
	return doc
}

func TestExtractMarkdownFrontMatterTitle(t *testing.T) {
	doc := extractFixture(t)
	if doc.Title != "The Test Article" {
		t.Errorf("title = %q, want from front matter", doc.Title)
	}
	if doc.Format != render.FormatMarkdown {
		t.Errorf("format = %q, want markdown", doc.Format)
	}
}

func TestExtractMarkdownHeadingsBecomeSections(t *testing.T) {
	doc := extractFixture(t)
	titles := make([]string, 0, len(doc.Sections))
	for _, s := range doc.Sections {
		if s.Title != "" {
			titles = append(titles, s.Title)
		}
	}
	want := []string{"Introduction", "Details", "Conclusion"}
	if len(titles) != len(want) {
		t.Fatalf("section titles = %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("section %d title = %q, want %q", i, titles[i], want[i])
		}
	}
	// Heading levels are captured.
	for _, s := range doc.Sections {
		if s.Title == "Introduction" && s.Level != 1 {
			t.Errorf("Introduction level = %d, want 1", s.Level)
		}
		if s.Title == "Details" && s.Level != 2 {
			t.Errorf("Details level = %d, want 2", s.Level)
		}
	}
}

func TestExtractMarkdownStripsFormattingAndSkipsCode(t *testing.T) {
	full := extractFixture(t).Narration()

	// Links render as text, emphasis/inline-code markers are gone.
	for _, want := range []string{"link", "first paragraph", "inline code", "First bullet point", "A blockquote line."} {
		if !strings.Contains(full, want) {
			t.Errorf("narration missing %q:\n%s", want, full)
		}
	}
	for _, unwanted := range []string{"**", "`", "](http", "func main", "skip me", "title:"} {
		if strings.Contains(full, unwanted) {
			t.Errorf("narration should not contain %q:\n%s", unwanted, full)
		}
	}
}

func TestExtractMarkdownNoHeadingPreamble(t *testing.T) {
	doc, err := ExtractMarkdown("x.md", []byte("Just a paragraph.\n\nAnd another."))
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(doc.Sections) != 1 || doc.Sections[0].Title != "" {
		t.Fatalf("sections = %+v, want one untitled preamble section", doc.Sections)
	}
	if !strings.Contains(doc.Sections[0].Text, "Just a paragraph.") {
		t.Errorf("preamble text = %q", doc.Sections[0].Text)
	}
}

func TestSlugifyAndSectionID(t *testing.T) {
	if id := sectionID(2, "Hello, World!"); id != "sec-002-hello-world" {
		t.Errorf("sectionID = %q, want sec-002-hello-world", id)
	}
	if id := sectionID(3, ""); id != "sec-003" {
		t.Errorf("untitled sectionID = %q, want sec-003", id)
	}
}

func TestExtractMarkdownUnitsSectioned(t *testing.T) {
	doc, err := ExtractMarkdown("g.md", []byte("# Intro\n\nHello.\n\n# Body\n\nWorld.\n"))
	if err != nil {
		t.Fatal(err)
	}
	units := doc.Units()
	if len(units) != 2 {
		t.Fatalf("units = %d, want 2", len(units))
	}
	if units[0].ID == "" || units[0].Title != "Intro" {
		t.Errorf("unit0 = %+v", units[0])
	}
	if units[1].Title != "Body" {
		t.Errorf("unit1 = %+v", units[1])
	}
}
