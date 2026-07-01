package render

import (
	"context"
	"path/filepath"
	"testing"
)

// idSynth is a fake synthesizer that reports a TTS-engine identity (so resume
// keys fold it in) and counts synthesis calls.
type idSynth struct {
	rate  int
	id    string
	calls int
}

func (s *idSynth) Synthesize(_ context.Context, text string) ([]float32, int, error) {
	s.calls++
	return make([]float32, len(text)), s.rate, nil
}

func (s *idSynth) Identity() string { return s.id }

// TestResumeKeyDistinguishesRenderInputs documents exactly which inputs the
// resume key folds in: changing any render-affecting input must change the key,
// while incidental whitespace must not.
func TestResumeKeyDistinguishesRenderInputs(t *testing.T) {
	base := Options{Input: "doc.epub", Format: FormatEPUB, Voice: "af_bella", Speed: 1.0}
	key := resumeKey(base, "kokoro", "Chapter text.", "001.wav")

	if got := resumeKey(base, "kokoro", "Chapter text.", "001.wav"); got != key {
		t.Fatal("identical inputs produced different resume keys")
	}

	cases := []struct {
		name string
		got  string
	}{
		{"text", resumeKey(base, "kokoro", "Different text.", "001.wav")},
		{"voice", resumeKey(withVoice(base, "af_sky"), "kokoro", "Chapter text.", "001.wav")},
		{"speed", resumeKey(withSpeed(base, 1.25), "kokoro", "Chapter text.", "001.wav")},
		{"provider", resumeKey(base, "fish", "Chapter text.", "001.wav")},
		{"format", resumeKey(withFormat(base, FormatText), "kokoro", "Chapter text.", "001.wav")},
		{"output", resumeKey(base, "kokoro", "Chapter text.", "002.wav")},
		{"source", resumeKey(withInput(base, "other.epub"), "kokoro", "Chapter text.", "001.wav")},
	}
	for _, c := range cases {
		if c.got == key {
			t.Errorf("%s change did not alter the resume key", c.name)
		}
	}

	// Whitespace-only differences inside a paragraph normalize away (same audio,
	// same key).
	if got := resumeKey(base, "kokoro", "Chapter   text.", "001.wav"); got != key {
		t.Error("whitespace normalization should not change the resume key")
	}
	if got := resumeKey(base, "kokoro", "Chapter\n\ntext.", "001.wav"); got == key {
		t.Error("paragraph boundary change should alter the resume key")
	}
}

func withVoice(o Options, v string) Options  { o.Voice = v; return o }
func withSpeed(o Options, s float64) Options { o.Speed = s; return o }
func withFormat(o Options, f Format) Options { o.Format = f; return o }
func withInput(o Options, in string) Options { o.Input = in; return o }

// TestRenderTextResumeSkipsUnchanged covers the single-file resume path: an
// unchanged output is skipped, and a render-affecting change re-synthesizes.
func TestRenderTextResumeSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.wav")
	opts := Options{Out: out, Format: FormatText, Voice: "af_bella", Speed: 1.0, Input: "in.txt"}
	synth := &fakeSynth{rate: 24000}
	var written []string

	res, err := RenderText(context.Background(), opts, "Hello world.", synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if err := WriteManifest(opts.ManifestPath(), res.Manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	firstCalls := len(synth.calls)
	if firstCalls == 0 {
		t.Fatal("expected synthesis on first render")
	}

	// Resume with identical inputs -> skipped, no new synthesis.
	resumeOpts := opts
	resumeOpts.Resume = true
	res2, err := RenderText(context.Background(), resumeOpts, "Hello world.", synth, recordingWriter(&written))
	if err != nil {
		t.Fatalf("resume render: %v", err)
	}
	if len(synth.calls) != firstCalls {
		t.Errorf("resume re-synthesized: calls %d -> %d", firstCalls, len(synth.calls))
	}
	if len(res2.Manifest.Segments) != 1 || res2.Manifest.Segments[0].Status != StatusSkipped {
		t.Errorf("resume manifest = %+v, want one skipped segment", res2.Manifest.Segments)
	}

	// Changing the voice invalidates resume even with identical text.
	revoice := resumeOpts
	revoice.Voice = "af_sky"
	if _, err := RenderText(context.Background(), revoice, "Hello world.", synth, recordingWriter(&written)); err != nil {
		t.Fatalf("revoice render: %v", err)
	}
	if len(synth.calls) == firstCalls {
		t.Error("voice change should have re-synthesized")
	}
}

// TestRenderChaptersResumeInvalidatesOnProviderChange proves the chapter resume
// key is composite: the same text/voice/speed under a different TTS engine is
// re-rendered, not skipped.
func TestRenderChaptersResumeInvalidatesOnProviderChange(t *testing.T) {
	dir := t.TempDir()
	opts := Options{OutDir: dir, Format: FormatEPUB, Voice: "af_bella", Speed: 1.0}
	chapters := sampleChapters()

	synthA := &idSynth{rate: 24000, id: "kokoro"}
	m1, err := RenderChapters(context.Background(), opts, chapters, synthA, recordingWriter(new([]string)))
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if err := WriteManifest(opts.ManifestPath(), m1); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	resumeOpts := opts
	resumeOpts.Resume = true
	synthB := &idSynth{rate: 24000, id: "fish"}
	m2, err := RenderChapters(context.Background(), resumeOpts, chapters, synthB, recordingWriter(new([]string)))
	if err != nil {
		t.Fatalf("resume render: %v", err)
	}
	complete, skipped, _ := m2.Counts()
	if skipped != 0 || complete != len(chapters) {
		t.Errorf("provider change: complete=%d skipped=%d, want all %d re-rendered", complete, skipped, len(chapters))
	}
	if synthB.calls == 0 {
		t.Error("expected the new provider to be used")
	}
}
