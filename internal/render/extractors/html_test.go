package extractors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
)

func extractHTMLFixture(t *testing.T) render.Document {
	t.Helper()
	path := filepath.Join("..", "..", "..", "tests", "fixtures", "documents", "article.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := ExtractHTML(path, data)
	if err != nil {
		t.Fatalf("ExtractHTML() error = %v", err)
	}
	return doc
}

func TestExtractHTMLTitleAndFormat(t *testing.T) {
	doc := extractHTMLFixture(t)
	if doc.Title != "The HTML Test Article" {
		t.Errorf("title = %q, want from <title>", doc.Title)
	}
	if doc.Format != render.FormatHTML {
		t.Errorf("format = %q, want html", doc.Format)
	}
}

func TestExtractHTMLDropsBoilerplateAndKeepsArticle(t *testing.T) {
	full := extractHTMLFixture(t).Narration()

	for _, want := range []string{"Main Title", "first paragraph", "Second paragraph", "Details Section", "First item", "A quoted line.", "A descriptive image caption", "&"} {
		if !strings.Contains(full, want) {
			t.Errorf("narration missing %q:\n%s", want, full)
		}
	}
	for _, unwanted := range []string{"console.log", "should be stripped", "Site Banner", "footer boilerplate", "color: red", "Home", "About", "<strong>", "&amp;", "a comment that should vanish"} {
		if strings.Contains(full, unwanted) {
			t.Errorf("narration should not contain %q:\n%s", unwanted, full)
		}
	}
}

func TestExtractHTMLPreservesOrder(t *testing.T) {
	full := extractHTMLFixture(t).Narration()
	main := strings.Index(full, "Main Title")
	details := strings.Index(full, "Details Section")
	if main < 0 || details < 0 || main > details {
		t.Errorf("article order not preserved (main=%d details=%d):\n%s", main, details, full)
	}
}
