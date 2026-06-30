package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RenderChapter is one render unit for multi-file (chaptered) output.
type RenderChapter struct {
	ID    string
	Title string
	Text  string
}

// RenderChapters synthesizes each chapter into its own WAV file under
// opts.OutDir and returns a manifest with one segment per chapter. Filenames are
// deterministic and filesystem-safe ("NNN-slug.wav"). With opts.Resume (and not
// opts.Overwrite), a chapter whose prior manifest entry has the same text hash
// and whose WAV still exists is skipped. It performs no STT/brain/mic/playback
// work and returns promptly on cancellation.
func RenderChapters(ctx context.Context, opts Options, chapters []RenderChapter, synth Synthesizer, writeWAV WAVWriter) (RenderManifest, error) {
	if opts.OutDir == "" {
		return RenderManifest{}, fmt.Errorf("render: chaptered rendering requires --out-dir DIR")
	}
	if len(chapters) == 0 {
		return RenderManifest{}, fmt.Errorf("render: no chapters to render")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return RenderManifest{}, fmt.Errorf("render: %w", err)
	}

	prior := map[string]ManifestSegment{}
	if opts.Resume && !opts.Overwrite {
		prior = priorSegmentsByOutput(opts.ManifestPath())
	}
	synthID := synthIdentity(synth)

	segs := make([]ManifestSegment, 0, len(chapters))
	sampleRate := 0
	var firstErr error
	failed := 0
	for i, ch := range chapters {
		// Cancellation stops promptly but still returns the chapters completed so
		// far, so the caller can persist a partial manifest and resume later.
		if err := ctx.Err(); err != nil {
			return chapterManifest(opts, segs, sampleRate), err
		}
		name := chapterFilename(i+1, ch)
		outPath := filepath.Join(opts.OutDir, name)
		hash := textHash(ch.Text)
		key := resumeKey(opts, synthID, ch.Text, name)
		base := ManifestSegment{
			Index: i + 1, ID: chapterID(ch, i+1), Title: ch.Title,
			TextSHA256: hash, ResumeKey: key, Output: name,
		}

		if p, ok := prior[name]; ok && resumable(p, key, outPath) {
			base.DurationMS = p.DurationMS
			base.Status = StatusSkipped
			segs = append(segs, base)
			continue
		}

		samples, rate, err := synthSegments(ctx, SegmentText(ch.Text, defaultMaxSegmentChars), synth)
		if err == nil {
			err = writeWAV(outPath, rate, samples)
		}
		if err != nil {
			// One bad chapter is recorded as failed and kept visible; the rest of
			// a long render still proceeds. Failed chapters carry no resume key, so
			// they are always retried on --resume.
			if firstErr == nil {
				firstErr = fmt.Errorf("render: chapter %d (%s): %w", i+1, name, err)
			}
			failed++
			base.ResumeKey = ""
			base.Status = StatusFailed
			segs = append(segs, base)
			continue
		}
		if sampleRate == 0 {
			sampleRate = rate
		}
		base.DurationMS = samplesDurationMS(len(samples), rate)
		base.Status = StatusComplete
		segs = append(segs, base)
	}

	manifest := chapterManifest(opts, segs, sampleRate)
	if failed > 0 {
		return manifest, fmt.Errorf("render: %d of %d chapter(s) failed: %w", failed, len(chapters), firstErr)
	}
	return manifest, nil
}

// chapterManifest assembles the render manifest from the per-chapter segments.
func chapterManifest(opts Options, segs []ManifestSegment, sampleRate int) RenderManifest {
	return RenderManifest{
		Schema:       RenderSchema,
		Title:        opts.Title,
		Source:       sourceLabel(opts),
		SourceFormat: opts.ResolveFormat(),
		Voice:        opts.Voice,
		SpeechSpeed:  opts.Speed,
		SampleRate:   sampleRate,
		Segments:     segs,
	}
}

// synthSegments synthesizes and concatenates the given text segments.
func synthSegments(ctx context.Context, segments []string, synth Synthesizer) ([]float32, int, error) {
	var all []float32
	rate := 0
	for i, seg := range segments {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		samples, r, err := synth.Synthesize(ctx, seg)
		if err != nil {
			return nil, 0, fmt.Errorf("synthesize segment %d: %w", i+1, err)
		}
		if rate == 0 {
			rate = r
		}
		all = append(all, samples...)
	}
	return all, rate, nil
}

func chapterID(ch RenderChapter, index int) string {
	if ch.ID != "" {
		return ch.ID
	}
	return fmt.Sprintf("ch-%03d", index)
}

func chapterFilename(index int, ch RenderChapter) string {
	slug := slugify(ch.Title)
	if slug == "" {
		slug = slugify(ch.ID)
	}
	if slug == "" {
		return fmt.Sprintf("%03d.wav", index)
	}
	return fmt.Sprintf("%03d-%s.wav", index, slug)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
