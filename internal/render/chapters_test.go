package render

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func sampleChapters() []RenderChapter {
	return []RenderChapter{
		{ID: "ch1", Title: "Chapter One", Text: "First chapter body."},
		{ID: "ch2", Title: "Chapter Two!", Text: "Second chapter body."},
	}
}

// recordingWriter records the WAV files written.
func recordingWriter(written *[]string) WAVWriter {
	return func(path string, rate int, samples []float32) error {
		*written = append(*written, path)
		// Touch the file so resume's existence check can see it.
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		return os.WriteFile(path, []byte("wav"), 0o644)
	}
}

func TestRenderChaptersWritesPerChapterFilesAndManifest(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB, Voice: "af_bella", Title: "Book"}
	synth := &fakeSynth{rate: 24000}
	var written []string

	m, err := RenderChapters(context.Background(), opts, sampleChapters(), synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("RenderChapters() error = %v", err)
	}

	wantFiles := []string{
		filepath.Join(dir, "001-chapter-one.wav"),
		filepath.Join(dir, "002-chapter-two.wav"), // "!" slugified out
	}
	if len(written) != 2 || written[0] != wantFiles[0] || written[1] != wantFiles[1] {
		t.Fatalf("written = %v, want %v", written, wantFiles)
	}

	if m.Schema != RenderSchema || m.Title != "Book" || m.SampleRate != 24000 {
		t.Errorf("manifest header = %+v", m)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
	for i, s := range m.Segments {
		if s.Index != i+1 || s.Status != StatusComplete || s.TextSHA256 == "" || s.Output == "" {
			t.Errorf("segment %d = %+v", i, s)
		}
	}
	if m.Segments[0].Title != "Chapter One" || m.Segments[0].Output != "001-chapter-one.wav" {
		t.Errorf("segment 0 = %+v", m.Segments[0])
	}
}

func TestRenderChaptersResumeSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB}
	chapters := sampleChapters()
	synth := &fakeSynth{rate: 24000}

	// First render: write the WAVs and the manifest.
	var w1 []string
	m1, err := RenderChapters(context.Background(), opts, chapters, synth, recordingWriter(&w1))
	if err != nil {
		t.Fatalf("first render error = %v", err)
	}
	if err := WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Second render with --resume: unchanged chapters should be skipped (not written).
	resumeOpts := opts
	resumeOpts.Resume = true
	var w2 []string
	m2, err := RenderChapters(context.Background(), resumeOpts, chapters, synth, recordingWriter(&w2))
	if err != nil {
		t.Fatalf("resume render error = %v", err)
	}
	if len(w2) != 0 {
		t.Errorf("resume wrote %v, want nothing re-rendered", w2)
	}
	_, skipped, _ := m2.Counts()
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 on unchanged resume", skipped)
	}

	// A changed chapter is re-rendered even on resume.
	changed := append([]RenderChapter(nil), chapters...)
	changed[1].Text = "Different second chapter body."
	var w3 []string
	m3, err := RenderChapters(context.Background(), resumeOpts, changed, synth, recordingWriter(&w3))
	if err != nil {
		t.Fatalf("changed resume error = %v", err)
	}
	if len(w3) != 1 {
		t.Errorf("changed resume wrote %v, want only the changed chapter", w3)
	}
	complete, skipped, _ := m3.Counts()
	if complete != 1 || skipped != 1 {
		t.Errorf("changed resume counts: complete=%d skipped=%d, want 1/1", complete, skipped)
	}
}

func TestRenderChaptersRequiresOutDir(t *testing.T) {
	_, err := RenderChapters(context.Background(), Options{Out: "x.wav"}, sampleChapters(), &fakeSynth{rate: 24000}, func(string, int, []float32) error { return nil })
	if err == nil {
		t.Fatal("RenderChapters without --out-dir should error")
	}
}

func TestChapterFilenameFallbacks(t *testing.T) {
	if got := chapterFilename(1, RenderChapter{Title: "Hello World"}); got != "001-hello-world.wav" {
		t.Errorf("filename = %q", got)
	}
	if got := chapterFilename(2, RenderChapter{ID: "ch2"}); got != "002-ch2.wav" {
		t.Errorf("id-fallback filename = %q", got)
	}
	if got := chapterFilename(3, RenderChapter{}); got != "003.wav" {
		t.Errorf("index-only filename = %q", got)
	}
}
