package render

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedSynth fails synthesis for specific texts and can cancel a context
// after the first successful call, to drive deterministic failure/interrupt
// tests without real audio.
type scriptedSynth struct {
	rate     int
	failText map[string]bool
	cancel   context.CancelFunc
	calls    int
}

func (s *scriptedSynth) Synthesize(_ context.Context, text string) ([]float32, int, error) {
	s.calls++
	if s.failText[text] {
		return nil, 0, errors.New("synthetic synth failure")
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return make([]float32, len(text)), s.rate, nil
}

func threeChapters() []RenderChapter {
	return []RenderChapter{
		{ID: "c1", Title: "One", Text: "Chapter one body."},
		{ID: "c2", Title: "Two", Text: "Chapter two body."},
		{ID: "c3", Title: "Three", Text: "Chapter three body."},
	}
}

// TestRenderChaptersContinuesOnChapterFailure proves continue-on-failure: a bad
// chapter is recorded as failed and kept visible while the others render, and
// the manifest distinguishes completed/failed counts.
func TestRenderChaptersContinuesOnChapterFailure(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB}
	synth := &scriptedSynth{rate: 24000, failText: map[string]bool{"Chapter two body.": true}}
	var written []string

	m, err := RenderChapters(context.Background(), opts, threeChapters(), synth, recordingWriter(&written))
	if err == nil {
		t.Fatal("expected an error when a chapter fails")
	}
	if !strings.Contains(err.Error(), "chapter 2") {
		t.Errorf("error %q should name the failed chapter", err)
	}

	complete, skipped, failed := m.Counts()
	if complete != 2 || skipped != 0 || failed != 1 {
		t.Errorf("counts complete=%d skipped=%d failed=%d, want 2/0/1", complete, skipped, failed)
	}
	if m.Segments[1].Status != StatusFailed || m.Segments[1].ResumeKey != "" {
		t.Errorf("failed segment = %+v, want failed status and no resume key", m.Segments[1])
	}
	// The failed chapter's WAV is not written; the others are.
	if len(written) != 2 {
		t.Errorf("written %v, want 2 (the failed chapter is skipped)", written)
	}
}

// TestRenderChaptersCancellationReturnsPartialManifest proves an interrupted
// render returns the chapters completed so far so the caller can persist a
// partial manifest and resume.
func TestRenderChaptersCancellationReturnsPartialManifest(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel right after the first chapter synthesizes, so chapter 2's loop
	// iteration observes cancellation.
	synth := &scriptedSynth{rate: 24000, cancel: cancel}
	var written []string

	m, err := RenderChapters(ctx, opts, threeChapters(), synth, recordingWriter(&written))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	complete, _, _ := m.Counts()
	if complete != 1 {
		t.Errorf("partial manifest completed=%d, want 1", complete)
	}
	if len(m.Segments) != 1 || m.Segments[0].Status != StatusComplete {
		t.Errorf("partial segments = %+v, want one completed", m.Segments)
	}
}

// TestRenderChaptersResumeAfterPartialFailure proves the full recovery loop:
// a first render that fails one chapter, persisted, then a resume with a working
// synth skips the completed chapters and retries only the failed one.
func TestRenderChaptersResumeAfterPartialFailure(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB}
	chapters := threeChapters()

	// First pass: chapter 2 fails.
	bad := &scriptedSynth{rate: 24000, failText: map[string]bool{"Chapter two body.": true}}
	m1, err := RenderChapters(context.Background(), opts, chapters, bad, recordingWriter(new([]string)))
	if err == nil {
		t.Fatal("first pass should report the failed chapter")
	}
	if err := WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatalf("persist partial manifest: %v", err)
	}

	// Resume with a healthy synth: chapters 1 and 3 skip, chapter 2 retries.
	resumeOpts := opts
	resumeOpts.Resume = true
	good := &scriptedSynth{rate: 24000}
	var written []string
	m2, err := RenderChapters(context.Background(), resumeOpts, chapters, good, recordingWriter(&written))
	if err != nil {
		t.Fatalf("resume after partial failure: %v", err)
	}
	complete, skipped, failed := m2.Counts()
	if complete != 1 || skipped != 2 || failed != 0 {
		t.Errorf("resume counts complete=%d skipped=%d failed=%d, want 1/2/0", complete, skipped, failed)
	}
	// Only the previously failed chapter is (re)written.
	if len(written) != 1 || filepath.Base(written[0]) != "002-two.wav" {
		t.Errorf("written %v, want only 002-two.wav retried", written)
	}
}

// TestManifestCountsDistinguishStatuses backs the --json completed/skipped/failed
// summary the command reports from Counts().
func TestManifestCountsDistinguishStatuses(t *testing.T) {
	m := RenderManifest{Segments: []ManifestSegment{
		{Status: StatusComplete}, {Status: StatusComplete},
		{Status: StatusSkipped},
		{Status: StatusFailed},
	}}
	complete, skipped, failed := m.Counts()
	if complete != 2 || skipped != 1 || failed != 1 {
		t.Errorf("Counts() = %d/%d/%d, want 2/1/1", complete, skipped, failed)
	}
}
