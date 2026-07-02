package extractors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturesDir() string {
	return filepath.Join("..", "..", "..", "tests", "fixtures", "documents")
}

// TestArticleGolden extracts the representative blog-post fixture and compares
// its narration to a golden file. Run with UPDATE_GOLDEN=1 to regenerate.
func TestArticleGolden(t *testing.T) {
	htmlPath := filepath.Join(fixturesDir(), "blog-post.html")
	goldenPath := filepath.Join(fixturesDir(), "blog-post.golden.txt")

	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := ExtractHTML(htmlPath, data)
	if err != nil {
		t.Fatalf("ExtractHTML() error = %v", err)
	}
	got := doc.Narration()

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got+"\n"), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
		t.Errorf("narration does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestArticleStructureAndBoilerplate asserts the document metadata and that
// navigation/script/sidebar boilerplate is absent from the narration.
func TestArticleStructureAndBoilerplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixturesDir(), "blog-post.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := ExtractHTML("blog-post.html", data)
	if err != nil {
		t.Fatalf("ExtractHTML() error = %v", err)
	}

	if doc.Title != "Why Local Voice AI Matters" {
		t.Errorf("title = %q, want the <title>", doc.Title)
	}

	// Article headings became sections in order.
	var headings []string
	for _, s := range doc.Sections {
		if s.Title != "" {
			headings = append(headings, s.Title)
		}
	}
	for _, want := range []string{"Why Local Voice AI Matters", "Latency", "Cost"} {
		found := false
		for _, h := range headings {
			if h == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing heading %q in %v", want, headings)
		}
	}

	full := doc.Narration()
	for _, want := range []string{"your data private", "No network hop", "Own your tools and your data.", "A diagram of the local pipeline"} {
		if !strings.Contains(full, want) {
			t.Errorf("narration missing %q:\n%s", want, full)
		}
	}
	for _, unwanted := range []string{"analytics.js", "Home", "Contact", "Sidebar link", "Tweet", "All rights reserved", "tracking pixel", "Example Tech Blog"} {
		if strings.Contains(full, unwanted) {
			t.Errorf("narration should not contain boilerplate %q:\n%s", unwanted, full)
		}
	}
}
