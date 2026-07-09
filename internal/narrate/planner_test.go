package narrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanMarkdownSections(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "article.md")
	if err := os.WriteFile(src, []byte("# Intro\n\nHello.\n\n# Body\n\nWorld.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "plan.yaml")
	res, err := BuildPlan(context.Background(), PlanOptions{Input: src, Out: out}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SectionCount != 2 || res.Format != "markdown" {
		t.Fatalf("result = %+v", res)
	}
	plan, err := Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sections[0].Title != "Intro" || plan.Sections[1].Title != "Body" {
		t.Fatalf("sections = %+v", plan.Sections)
	}
	// extracted files exist
	for _, s := range plan.Sections {
		p := filepath.Join(dir, s.ExtractedPath)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing extracted %s: %v", p, err)
		}
	}
	// overwrite protection
	if _, err := BuildPlan(context.Background(), PlanOptions{Input: src, Out: out}, nil); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("expected overwrite error, got %v", err)
	}
}

func TestPDFExtractorMissingBinary(t *testing.T) {
	e := PDFExtractor{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	_, _, err := e.ExtractPages(context.Background(), "x.pdf")
	if err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFExtractorFakeRunner(t *testing.T) {
	e := PDFExtractor{
		LookPath: func(string) (string, error) { return "/bin/pdftotext", nil },
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("Page one text.\fPage two text."), nil
		},
	}
	pages, _, err := e.ExtractPages(context.Background(), "x.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Page != 1 || !strings.Contains(pages[1].Text, "two") {
		t.Fatalf("pages = %+v", pages)
	}
}
