package render

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRenderChaptersSkipsEmptyChapterWithoutWritingWAV(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB, Title: "Book"}
	synth := &fakeSynth{rate: 24000}
	var written []string

	chapters := []RenderChapter{
		{ID: "ch1", Title: "Cover", Text: "   "}, // no narratable text
		{ID: "ch2", Title: "Body", Text: "Real content here."},
	}
	m, err := RenderChapters(context.Background(), opts, chapters, synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("RenderChapters() error = %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("wrote %d WAV(s), want 1 (empty chapter must not be written): %v", len(written), written)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
	empty := m.Segments[0]
	if empty.Status != StatusSkipped || empty.Output != "" || empty.ResumeKey != "" {
		t.Errorf("empty chapter segment = %+v, want skipped with no output/resume key", empty)
	}
	if m.Segments[1].Status != StatusComplete {
		t.Errorf("second chapter status = %q, want complete", m.Segments[1].Status)
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

// TestChapterFilenameLocksSlugRules locks the exact slug rules behind generated
// WAV filenames (case folding, punctuation collapsing, unicode stripping, the
// 40-char cap, dash trimming). A refactor that changes any of these silently
// breaks resume against existing output directories.
func TestChapterFilenameLocksSlugRules(t *testing.T) {
	cases := []struct {
		name  string
		index int
		ch    RenderChapter
		want  string
	}{
		{"unicode-only title falls back to id", 4, RenderChapter{ID: "ch1", Title: "第一章"}, "004-ch1.wav"},
		{"punctuation collapses to single dashes", 1, RenderChapter{Title: "What's New?? (2025 Edition)"}, "001-what-s-new-2025-edition.wav"},
		{"case folds and whitespace dashes", 2, RenderChapter{Title: "THE Quick   Brown Fox"}, "002-the-quick-brown-fox.wav"},
		{"long title caps at 40 chars", 3, RenderChapter{Title: strings.Repeat("abcde ", 10)}, "003-abcde-abcde-abcde-abcde-abcde-abcde-abcd.wav"},
		{"cap trims a trailing dash", 5, RenderChapter{Title: strings.Repeat("abcd ", 12)}, "005-abcd-abcd-abcd-abcd-abcd-abcd-abcd-abcd.wav"},
		{"title wins over id", 6, RenderChapter{ID: "xhtml-6", Title: "Epilogue"}, "006-epilogue.wav"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chapterFilename(c.index, c.ch); got != c.want {
				t.Errorf("chapterFilename(%d, %+v) = %q, want %q", c.index, c.ch, got, c.want)
			}
		})
	}
}

// TestRenderChaptersManifestGolden pins the entire chaptered manifest for a
// fixed input: chapter order, segment IDs (explicit and ch-NNN fallback),
// titles, output filenames, source_format, and header fields. RenderChapters
// refactors must reproduce this manifest exactly.
func TestRenderChaptersManifestGolden(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Input: "book.epub", OutDir: dir, Format: FormatEPUB, Voice: "af_bella", Speed: 1.0, Title: "Golden Book"}
	chapters := []RenderChapter{
		{ID: "intro", Title: "Introduction", Text: "Welcome to the golden book."},
		{Title: "Chapter One!", Text: "First chapter body."},
		{ID: "ch-three", Text: "Third chapter body."},
	}

	m, err := RenderChapters(context.Background(), opts, chapters, &fakeSynth{rate: 24000}, recordingWriter(new([]string)))
	if err != nil {
		t.Fatalf("RenderChapters() error = %v", err)
	}

	want := RenderManifest{
		Schema:       RenderSchema,
		Title:        "Golden Book",
		Source:       "book.epub",
		SourceFormat: FormatEPUB,
		Voice:        "af_bella",
		SpeechSpeed:  1.0,
		SampleRate:   24000,
		Segments: []ManifestSegment{
			{Index: 1, ID: "intro", Title: "Introduction", TextSHA256: textHash(chapters[0].Text),
				ResumeKey: resumeKey(opts, "", chapters[0].Text, "001-introduction.wav"),
				Output:    "001-introduction.wav", DurationMS: 1, Status: StatusComplete},
			{Index: 2, ID: "ch-002", Title: "Chapter One!", TextSHA256: textHash(chapters[1].Text),
				ResumeKey: resumeKey(opts, "", chapters[1].Text, "002-chapter-one.wav"),
				Output:    "002-chapter-one.wav", DurationMS: 0, Status: StatusComplete},
			{Index: 3, ID: "ch-three", TextSHA256: textHash(chapters[2].Text),
				ResumeKey: resumeKey(opts, "", chapters[2].Text, "003-ch-three.wav"),
				Output:    "003-ch-three.wav", DurationMS: 0, Status: StatusComplete},
		},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("manifest drifted from golden.\n got: %+v\nwant: %+v", m, want)
	}
}

// TestRenderChaptersAllSkippedResumeKeepsSampleRate guards the manifest rewrite
// on an all-skipped --resume rerun: the prior run's sample rate must be carried
// forward, not clobbered with 0.
func TestRenderChaptersAllSkippedResumeKeepsSampleRate(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB, Resume: true}
	synth := &fakeSynth{rate: 24000}
	var written []string

	m1, err := RenderChapters(context.Background(), opts, sampleChapters(), synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("fresh RenderChapters() error = %v", err)
	}
	if m1.SampleRate != 24000 {
		t.Fatalf("fresh sample rate = %d, want 24000", m1.SampleRate)
	}
	if err := WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatal(err)
	}

	m2, err := RenderChapters(context.Background(), opts, sampleChapters(), synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("resumed RenderChapters() error = %v", err)
	}
	complete, skipped, _ := m2.Counts()
	if complete != 0 || skipped != 2 {
		t.Fatalf("resume counts = %d complete / %d skipped, want 0/2", complete, skipped)
	}
	if m2.SampleRate != 24000 {
		t.Fatalf("resumed sample rate = %d, want 24000 (prior rate must be preserved)", m2.SampleRate)
	}
}
