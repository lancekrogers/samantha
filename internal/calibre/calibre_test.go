package calibre

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleListJSON = `[
  {
    "authors": "Peter Norvig & Stuart J. Russell",
    "formats": [
      "/lib/Peter Norvig/AI (5)/AI.epub",
      "/lib/Peter Norvig/AI (5)/AI.pdf"
    ],
    "id": 5,
    "pubdate": "2010-06-16T08:33:57+00:00",
    "series": "",
    "tags": ["AI", "Computing"],
    "title": "Artificial Intelligence A Modern Approach"
  },
  {
    "authors": "Laurens R. Krol",
    "formats": ["/lib/Laurens R. Krol/Crypto 101 (42)/Crypto 101.mobi"],
    "id": 42,
    "pubdate": "2013-01-01T00:00:00+00:00",
    "tags": ["Security"],
    "title": "Crypto 101"
  }
]`

func TestSearchParsesJSON(t *testing.T) {
	var gotArgs []string
	c := Client{
		LookPath: func(string) (string, error) { return "/bin/calibredb", nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "/bin/calibredb" {
				t.Fatalf("binary = %q", name)
			}
			gotArgs = append([]string{}, args...)
			return []byte(sampleListJSON), nil
		},
		LibraryPath: "/Users/me/Calibre Library",
	}
	books, err := c.Search(context.Background(), "cryptography", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("len(books)=%d", len(books))
	}
	if books[0].ID != 5 || books[0].Title == "" {
		t.Fatalf("book0 = %+v", books[0])
	}
	if len(books[0].Authors) != 2 {
		t.Fatalf("authors = %v", books[0].Authors)
	}
	if len(books[0].Formats) != 2 {
		t.Fatalf("formats = %v", books[0].Formats)
	}
	// Fixed argv: query is a single element, no shell.
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--search cryptography") {
		t.Fatalf("args missing search: %v", gotArgs)
	}
	if !strings.Contains(joined, "--with-library /Users/me/Calibre Library") {
		t.Fatalf("args missing library: %v", gotArgs)
	}
	if !strings.Contains(joined, "--for-machine") {
		t.Fatalf("args missing --for-machine: %v", gotArgs)
	}
}

func TestBestFormatPathPrefersEPUB(t *testing.T) {
	c := Client{Prefer: "epub"}
	dir := t.TempDir()
	epub := filepath.Join(dir, "book.epub")
	pdf := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(epub, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Book{
		ID:    1,
		Title: "T",
		Formats: []string{
			pdf,
			epub,
			"/x/book.mobi",
		},
	}
	path, format, err := c.BestFormatPath(b)
	if err != nil {
		t.Fatal(err)
	}
	if format != "epub" || !strings.HasSuffix(path, ".epub") {
		t.Fatalf("got %s %s", path, format)
	}
}

func TestBestFormatPathPDFFallback(t *testing.T) {
	c := Client{}
	pdf := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(pdf, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Book{ID: 1, Title: "T", Formats: []string{"/x/book.mobi", pdf}}
	path, format, err := c.BestFormatPath(b)
	if err != nil {
		t.Fatal(err)
	}
	if format != "pdf" || filepath.Ext(path) != ".pdf" {
		t.Fatalf("got %s %s", path, format)
	}
}

func TestBestFormatPathFallsBackWhenPreferredFileIsStale(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(pdf, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Client{Prefer: "epub"}
	b := Book{ID: 1, Title: "T", Formats: []string{filepath.Join(dir, "missing.epub"), pdf}}
	path, format, err := c.BestFormatPath(b)
	if err != nil {
		t.Fatal(err)
	}
	if path != pdf || format != "pdf" {
		t.Fatalf("got %s %s, want %s pdf", path, format, pdf)
	}
}

func TestBestFormatPathReportsMissingSupportedFile(t *testing.T) {
	c := Client{}
	b := Book{ID: 1, Title: "T", Formats: []string{"/missing/book.epub"}}
	_, _, err := c.BestFormatPath(b)
	if !errors.Is(err, ErrFormatMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestBestFormatPathMOBIOnly(t *testing.T) {
	c := Client{}
	b := Book{ID: 42, Title: "Crypto 101", Formats: []string{"/x/book.mobi", "/x/book.azw3"}}
	_, _, err := c.BestFormatPath(b)
	if !errors.Is(err, ErrNoSupportedFormat) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveSingle(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"id":1,"title":"Only","authors":"A","formats":["/a.epub"],"tags":[]}]`), nil
		},
	}
	b, err := c.Resolve(context.Background(), "Only")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != 1 {
		t.Fatalf("id=%d", b.ID)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(sampleListJSON), nil
		},
	}
	_, err := c.Resolve(context.Background(), "crypto")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveNotFound(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[]`), nil
		},
	}
	_, err := c.Resolve(context.Background(), "zzzz")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchBinaryNotFound(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "", ErrCalibreNotFound },
	}
	_, err := c.Search(context.Background(), "x", 5)
	if !errors.Is(err, ErrCalibreNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("Run should not be called")
			return nil, nil
		},
	}
	_, err := c.Search(ctx, "x", 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := Client{}
	_, err := c.Search(context.Background(), "  ", 5)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMetadata(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "id:5") {
				t.Fatalf("args = %v", args)
			}
			return []byte(`[{"id":5,"title":"AI","authors":"P","formats":["/a.epub"],"tags":["AI"]}]`), nil
		},
	}
	b, err := c.Metadata(context.Background(), 5)
	if err != nil || b.ID != 5 {
		t.Fatalf("got %+v err=%v", b, err)
	}
}

func TestFullTextSearch(t *testing.T) {
	c := Client{
		LookPath: func(string) (string, error) { return "calibredb", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"book_id":5,"title":"AI","snippet":"... goroutine ..."}]`), nil
		},
	}
	hits, err := c.FullTextSearch(context.Background(), "goroutine", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].BookID != 5 {
		t.Fatalf("hits=%+v", hits)
	}
}

func TestParseListAuthorsAsArray(t *testing.T) {
	data := []byte(`[{"id":1,"title":"T","authors":["A","B"],"formats":["/t.epub"],"tags":["x"]}]`)
	books, err := parseListJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(books[0].Authors) != 2 {
		t.Fatalf("authors=%v", books[0].Authors)
	}
}

func TestBundleLookPathEmpty(t *testing.T) {
	_, err := BundleLookPath("")
	if !errors.Is(err, ErrCalibreNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestDefaultSearchTimeoutApplied(t *testing.T) {
	// Ensure Search with a background context still completes via fake Run.
	c := Client{
		SearchTimeout: 50 * time.Millisecond,
		LookPath:      func(string) (string, error) { return "calibredb", nil },
		Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected deadline")
			}
			return []byte(`[]`), nil
		},
	}
	_, err := c.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatal(err)
	}
}
