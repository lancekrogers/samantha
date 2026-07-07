package render_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"github.com/lancekrogers/samantha/internal/render/extractors"
)

const (
	tinyBookFixture = "../../tests/fixtures/documents/tiny-book.epub"
	tinyBookGolden  = "../../tests/fixtures/documents/tiny-book.manifest.golden.json"
)

// fixtureChapters parses the tiny EPUB fixture into render chapters the same
// way runRenderEPUB does (spine order, NCX titles, HTML narration).
func fixtureChapters(t *testing.T) (*epub.Book, []render.RenderChapter) {
	t.Helper()
	zr, err := zip.OpenReader(tinyBookFixture)
	if err != nil {
		t.Fatalf("open fixture %s: %v", tinyBookFixture, err)
	}
	t.Cleanup(func() { zr.Close() })

	book, err := epub.Parse(&zr.Reader)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
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
	return book, chapters
}

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
	book, chapters := fixtureChapters(t)

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
	// Segment IDs come from the OPF manifest item IDs, in spine order.
	if chapters[0].ID != "ch1" || chapters[1].ID != "ch2" {
		t.Errorf("chapter IDs = %q/%q, want ch1/ch2", chapters[0].ID, chapters[1].ID)
	}

	dir := t.TempDir()
	opts := render.Options{
		Input:  tinyBookFixture,
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

// TestTinyEPUBFixtureManifestGolden locks the serialized manifest of the
// fixture render byte-for-byte: JSON field names and order, chapter order,
// segment IDs, titles, output filenames, text hashes, resume keys,
// source_format, voice, and speed. Any silent change to what RenderChapters (or
// the extraction feeding it) produces fails this diff. Run with UPDATE_GOLDEN=1
// to regenerate after an intentional change.
func TestTinyEPUBFixtureManifestGolden(t *testing.T) {
	_, chapters := fixtureChapters(t)

	dir := t.TempDir()
	opts := render.Options{
		Input:  tinyBookFixture,
		OutDir: dir,
		Format: render.FormatEPUB,
		Voice:  "af_bella",
		Speed:  1.0,
		Title:  "Tiny Test Book",
	}
	manifest, err := render.RenderChapters(context.Background(), opts, chapters, fixtureSynth{rate: 24000}, render.WAVWriter(writeFixtureWAV))
	if err != nil {
		t.Fatalf("RenderChapters: %v", err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	if err := render.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(tinyBookGolden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(tinyBookGolden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("manifest does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestTinyEPUBFixtureResumeBehavior locks resume over the full fixture path:
// an unchanged rerun skips every chapter without writing, and a changed chapter
// re-renders to the same filename while the rest still skip.
func TestTinyEPUBFixtureResumeBehavior(t *testing.T) {
	_, chapters := fixtureChapters(t)

	dir := t.TempDir()
	opts := render.Options{
		Input:  tinyBookFixture,
		OutDir: dir,
		Format: render.FormatEPUB,
		Voice:  "af_bella",
		Speed:  1.0,
		Title:  "Tiny Test Book",
	}
	m1, err := render.RenderChapters(context.Background(), opts, chapters, fixtureSynth{rate: 24000}, render.WAVWriter(writeFixtureWAV))
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if err := render.WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Unchanged resume: everything skips, nothing is written.
	resumeOpts := opts
	resumeOpts.Resume = true
	noWrite := func(path string, _ int, _ []float32) error {
		t.Fatalf("unchanged resume wrote %s; want full skip", path)
		return nil
	}
	m2, err := render.RenderChapters(context.Background(), resumeOpts, chapters, fixtureSynth{rate: 24000}, render.WAVWriter(noWrite))
	if err != nil {
		t.Fatalf("unchanged resume: %v", err)
	}
	complete, skipped, _ := m2.Counts()
	if complete != 0 || skipped != len(chapters) {
		t.Errorf("unchanged resume counts complete=%d skipped=%d, want 0/%d", complete, skipped, len(chapters))
	}
	for i, seg := range m2.Segments {
		if seg.Output != m1.Segments[i].Output || seg.ID != m1.Segments[i].ID || seg.Title != m1.Segments[i].Title {
			t.Errorf("resumed segment %d = %+v, want identity of fresh segment %+v", i, seg, m1.Segments[i])
		}
	}

	// A changed chapter re-renders to the same filename; the other still skips.
	changed := append([]render.RenderChapter(nil), chapters...)
	changed[1].Text += " A brand new closing sentence."
	var written []string
	record := func(path string, _ int, _ []float32) error {
		written = append(written, path)
		return os.WriteFile(path, []byte("RIFFfixture"), 0o644)
	}
	m3, err := render.RenderChapters(context.Background(), resumeOpts, changed, fixtureSynth{rate: 24000}, render.WAVWriter(record))
	if err != nil {
		t.Fatalf("changed resume: %v", err)
	}
	if len(written) != 1 || filepath.Base(written[0]) != "002-chapter-two.wav" {
		t.Errorf("changed resume wrote %v, want only 002-chapter-two.wav", written)
	}
	complete, skipped, _ = m3.Counts()
	if complete != 1 || skipped != 1 {
		t.Errorf("changed resume counts complete=%d skipped=%d, want 1/1", complete, skipped)
	}
	if m3.Segments[1].Output != "002-chapter-two.wav" {
		t.Errorf("changed chapter output = %q, want stable filename 002-chapter-two.wav", m3.Segments[1].Output)
	}
}

// writeFixtureWAV is a minimal WAVWriter that just creates the file so the
// render path's existence checks and on-disk assertions hold without pulling in
// the cgo audio encoder.
func writeFixtureWAV(path string, _ int, _ []float32) error {
	return os.WriteFile(path, []byte("RIFFfixture"), 0o644)
}
