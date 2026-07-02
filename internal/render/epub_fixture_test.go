package render_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"github.com/lancekrogers/samantha/internal/render/extractors"
)

// fixtureSynth is a deterministic fake TTS for the fixture render: it returns a
// fixed number of silent samples per call so tests never need real models.
type fixtureSynth struct{ rate int }

func (s fixtureSynth) Synthesize(_ context.Context, _ string) ([]float32, int, error) {
	return make([]float32, 240), s.rate, nil
}

// TestTinyEPUBFixtureFullPath exercises the full parser-to-render path against
// the checked-in tiny EPUB fixture with fake TTS — no network, no large models.
// It mirrors the command's runRenderEPUB wiring so the fixture guards the same
// path users hit: zip -> epub.Parse -> ExtractHTML -> Narration ->
// RenderChapters, asserting spine order, nav titles, filenames, and manifest.
func TestTinyEPUBFixtureFullPath(t *testing.T) {
	const fixture = "../../tests/fixtures/documents/tiny-book.epub"

	zr, err := zip.OpenReader(fixture)
	if err != nil {
		t.Fatalf("open fixture %s: %v", fixture, err)
	}
	defer zr.Close()

	book, err := epub.Parse(&zr.Reader)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if book.Metadata.Title != "Tiny Test Book" {
		t.Errorf("title = %q, want %q", book.Metadata.Title, "Tiny Test Book")
	}
	if len(book.Chapters) != 2 {
		t.Fatalf("chapters = %d, want 2", len(book.Chapters))
	}
	// Spine order and nav (NCX) titles are authoritative.
	if book.Chapters[0].Title != "Chapter One" || book.Chapters[1].Title != "Chapter Two" {
		t.Errorf("chapter titles = %q/%q", book.Chapters[0].Title, book.Chapters[1].Title)
	}

	chapters := make([]render.RenderChapter, 0, len(book.Chapters))
	for _, ch := range book.Chapters {
		data, err := book.ReadChapter(ch.Href)
		if err != nil {
			t.Fatalf("read chapter %s: %v", ch.Href, err)
		}
		doc, err := extractors.ExtractHTML(ch.Href, data)
		if err != nil {
			t.Fatalf("extract %s: %v", ch.Href, err)
		}
		chapters = append(chapters, render.RenderChapter{ID: ch.ID, Title: ch.Title, Text: doc.Narration()})
	}

	dir := t.TempDir()
	opts := render.Options{
		Input:  fixture,
		OutDir: dir,
		Format: render.FormatEPUB,
		Title:  book.Metadata.Title,
	}
	manifest, err := render.RenderChapters(context.Background(), opts, chapters, fixtureSynth{rate: 24000}, render.WAVWriter(writeFixtureWAV))
	if err != nil {
		t.Fatalf("RenderChapters: %v", err)
	}

	// Manifest records chapter-to-file mapping in spine order.
	if manifest.Schema != render.RenderSchema || manifest.Title != "Tiny Test Book" {
		t.Errorf("manifest header = %+v", manifest)
	}
	wantOutputs := []string{"001-chapter-one.wav", "002-chapter-two.wav"}
	if len(manifest.Segments) != len(wantOutputs) {
		t.Fatalf("segments = %d, want %d", len(manifest.Segments), len(wantOutputs))
	}
	for i, seg := range manifest.Segments {
		if seg.Index != i+1 || seg.Output != wantOutputs[i] || seg.Status != render.StatusComplete {
			t.Errorf("segment %d = %+v, want output %q", i, seg, wantOutputs[i])
		}
		if _, err := os.Stat(filepath.Join(dir, seg.Output)); err != nil {
			t.Errorf("expected WAV %s on disk: %v", seg.Output, err)
		}
	}
	if manifest.Segments[0].Title != "Chapter One" || manifest.Segments[1].Title != "Chapter Two" {
		t.Errorf("manifest titles = %q/%q", manifest.Segments[0].Title, manifest.Segments[1].Title)
	}
}

// writeFixtureWAV is a minimal WAVWriter that just creates the file so the
// render path's existence checks and on-disk assertions hold without pulling in
// the cgo audio encoder.
func writeFixtureWAV(path string, _ int, _ []float32) error {
	return os.WriteFile(path, []byte("RIFFfixture"), 0o644)
}
