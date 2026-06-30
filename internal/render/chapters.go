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

	segs := make([]ManifestSegment, 0, len(chapters))
	sampleRate := 0
	for i, ch := range chapters {
		if err := ctx.Err(); err != nil {
			return RenderManifest{}, err
		}
		name := chapterFilename(i+1, ch)
		outPath := filepath.Join(opts.OutDir, name)
		hash := textHash(ch.Text)

		if p, ok := prior[name]; ok && p.TextSHA256 == hash && pathExists(outPath) {
			segs = append(segs, ManifestSegment{
				Index: i + 1, ID: chapterID(ch, i+1), Title: ch.Title,
				TextSHA256: hash, Output: name, DurationMS: p.DurationMS, Status: StatusSkipped,
			})
			continue
		}

		samples, rate, err := synthSegments(ctx, SegmentText(ch.Text, defaultMaxSegmentChars), synth)
		if err != nil {
			return RenderManifest{}, fmt.Errorf("render: chapter %d (%s): %w", i+1, name, err)
		}
		if sampleRate == 0 {
			sampleRate = rate
		}
		if err := writeWAV(outPath, rate, samples); err != nil {
			return RenderManifest{}, fmt.Errorf("render: write %s: %w", outPath, err)
		}
		segs = append(segs, ManifestSegment{
			Index: i + 1, ID: chapterID(ch, i+1), Title: ch.Title,
			TextSHA256: hash, Output: name, DurationMS: samplesDurationMS(len(samples), rate), Status: StatusComplete,
		})
	}

	return RenderManifest{
		Schema:       RenderSchema,
		Title:        opts.Title,
		Source:       sourceLabel(opts),
		SourceFormat: opts.ResolveFormat(),
		Voice:        opts.Voice,
		SpeechSpeed:  opts.Speed,
		SampleRate:   sampleRate,
		Segments:     segs,
	}, nil
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
