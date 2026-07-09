package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RenderUnit is one ordered renderable unit for multi-file output: an EPUB
// chapter, a document section, or any other independently narrated span.
// SourceRef optionally records where the unit came from (e.g. a chapter href);
// it does not affect rendered output.
type RenderUnit struct {
	ID        string
	Title     string
	Text      string
	SourceRef string
}

// RenderUnits synthesizes each unit into its own WAV file under opts.OutDir
// and returns a manifest with one segment per unit. Filenames are
// deterministic and filesystem-safe ("NNN-slug.wav"). With opts.Resume (and not
// opts.Overwrite), a unit whose prior manifest entry has the same text hash
// and whose WAV still exists is skipped. It performs no STT/brain/mic/playback
// work and returns promptly on cancellation.
//
// Error wording and the ch-NNN ID fallback still say "chapter": they are
// locked by the EPUB output compatibility tests until the chapter wrappers
// are removed.
func RenderUnits(ctx context.Context, opts Options, units []RenderUnit, synth Synthesizer, writeWAV WAVWriter) (RenderManifest, error) {
	if opts.OutDir == "" {
		return RenderManifest{}, fmt.Errorf("render: chaptered rendering requires --out-dir DIR")
	}
	if len(units) == 0 {
		return RenderManifest{}, fmt.Errorf("render: no chapters to render")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return RenderManifest{}, fmt.Errorf("render: %w", err)
	}

	prior := map[string]ManifestSegment{}
	priorRate := 0
	if opts.Resume && !opts.Overwrite {
		if m, ok := loadPriorManifest(opts.ManifestPath()); ok {
			for _, s := range m.Segments {
				prior[s.Output] = s
			}
			priorRate = m.SampleRate
		}
	}
	synthID := synthIdentity(synth)

	segs := make([]ManifestSegment, 0, len(units))
	sampleRate := 0
	var firstErr error
	failed := 0
	for i, u := range units {
		// Cancellation stops promptly but still returns the units completed so
		// far, so the caller can persist a partial manifest and resume later.
		if err := ctx.Err(); err != nil {
			if sampleRate == 0 {
				sampleRate = priorRate
			}
			return unitManifest(opts, segs, sampleRate), err
		}

		seg, rate, err := renderUnit(ctx, opts, i, u, synthID, prior, synth, writeWAV)
		if err != nil {
			// One bad unit is recorded as failed and kept visible; the rest of
			// a long render still proceeds. Failed units carry no resume key, so
			// they are always retried on --resume.
			if firstErr == nil {
				firstErr = err
			}
			failed++
		}
		if sampleRate == 0 {
			sampleRate = rate
		}
		segs = append(segs, seg)
	}

	// An all-skipped resume synthesizes nothing; carry the prior run's rate
	// forward instead of rewriting the manifest with sample_rate 0. Fresh audio
	// always wins when any unit was synthesized.
	if sampleRate == 0 {
		sampleRate = priorRate
	}

	manifest := unitManifest(opts, segs, sampleRate)
	if failed > 0 {
		return manifest, fmt.Errorf("render: %d of %d chapter(s) failed: %w", failed, len(units), firstErr)
	}
	return manifest, nil
}

// renderUnit renders one unit to its WAV (or skips it via resume /
// empty-narration rules) and returns its manifest segment plus the synthesized
// sample rate (0 when nothing was synthesized). A synthesis or write failure is
// returned as an error alongside a failed segment carrying no resume key.
func renderUnit(ctx context.Context, opts Options, i int, u RenderUnit, synthID string, prior map[string]ManifestSegment, synth Synthesizer, writeWAV WAVWriter) (ManifestSegment, int, error) {
	name := unitFilename(i+1, u)
	outPath := filepath.Join(opts.OutDir, name)
	base := ManifestSegment{
		Index: i + 1, ID: unitID(u, i+1), Title: u.Title,
		TextSHA256: textHash(u.Text), ResumeKey: resumeKey(opts, synthID, u.Text, name), Output: name,
	}

	if p, ok := prior[name]; ok && resumable(p, base.ResumeKey, outPath) {
		base.DurationMS = p.DurationMS
		base.Status = StatusSkipped
		return base, 0, nil
	}

	spans := PlanUnitSpans(opts, u.Text)
	speechCount := 0
	for _, sp := range spans {
		if sp.Kind == SpanSpeech && strings.TrimSpace(sp.Text) != "" {
			speechCount++
		}
	}
	if speechCount == 0 {
		// A unit with no narratable text (an image-only or fully
		// boilerplate-stripped page) produces no audio. Record it as skipped
		// with no output instead of writing a malformed zero-rate WAV that
		// would look complete and be resume-skipped forever.
		base.Status = StatusSkipped
		base.Output = ""
		base.ResumeKey = ""
		return base, 0, nil
	}

	samples, rate, err := synthSpans(ctx, spans, synth, requestMeta(opts, base.ID, u.Title), opts.Voice, opts.Speed)
	if err == nil {
		err = writeWAV(outPath, rate, samples)
	}
	if err != nil {
		base.ResumeKey = ""
		base.Status = StatusFailed
		return base, 0, fmt.Errorf("render: chapter %d (%s): %w", i+1, name, err)
	}

	base.DurationMS = samplesDurationMS(len(samples), rate)
	base.Status = StatusComplete
	return base, rate, nil
}

// unitManifest assembles the render manifest from the per-unit segments.
func unitManifest(opts Options, segs []ManifestSegment, sampleRate int) RenderManifest {
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

func unitID(u RenderUnit, index int) string {
	if u.ID != "" {
		return u.ID
	}
	return fmt.Sprintf("ch-%03d", index)
}

func unitFilename(index int, u RenderUnit) string {
	slug := Slugify(u.Title)
	if slug == "" {
		slug = Slugify(u.ID)
	}
	if slug == "" {
		return fmt.Sprintf("%03d.wav", index)
	}
	return fmt.Sprintf("%03d-%s.wav", index, slug)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases s and reduces it to a filesystem/ID-safe dash-separated
// slug capped at 40 characters. It is the single source of the slug rules used
// for unit filenames and extracted section IDs, which must stay in step for
// manifest-to-file correspondence.
func Slugify(s string) string {
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
