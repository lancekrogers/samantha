package render

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultMaxSegmentChars caps the text handed to a single TTS request.
const defaultMaxSegmentChars = 1500

// Synthesizer turns text into mono PCM samples at a sample rate. The command
// layer adapts the cgo TTS provider into this cgo-free interface so render
// orchestration is testable without models.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (samples []float32, sampleRate int, err error)
}

// SynthesisRequest is a typed batch-render synthesis call with optional
// voice/speed overrides and stable metadata for resume/manifest identity.
type SynthesisRequest struct {
	Text     string
	Voice    string
	Speed    float64
	Metadata map[string]string
}

// RequestSynthesizer is implemented by synthesizers that accept typed requests.
// When present, batch render prefers it over Synthesize so metadata reaches the
// TTS provider; the plain Synthesize path remains for tests and live voice.
type RequestSynthesizer interface {
	SynthesizeRequest(ctx context.Context, req SynthesisRequest) (samples []float32, sampleRate int, err error)
}

// WAVWriter writes mono float32 PCM to a WAV file. The command layer injects the
// real audio writer.
type WAVWriter func(path string, sampleRate int, samples []float32) error

// Result summarizes a completed single-file render.
type Result struct {
	Output     string
	Segments   int
	SampleRate int
	Samples    int
	Duration   time.Duration
	Manifest   RenderManifest
}

// RenderText synthesizes text into a single WAV file at opts.Out. It segments
// the text (paragraph-aware, size-capped), synthesizes each segment, and writes
// the concatenated audio. It also builds a render manifest (with per-segment
// text hashes and durations) that the caller can write. It performs no
// STT/brain/microphone/playback work and returns promptly on context
// cancellation.
func RenderText(ctx context.Context, opts Options, text string, synth Synthesizer, writeWAV WAVWriter) (Result, error) {
	if opts.Out == "" {
		return Result{}, fmt.Errorf("render: text rendering requires --out FILE")
	}
	spans := PlanTextSpans(opts, text)
	speechCount := 0
	for _, sp := range spans {
		if sp.Kind == SpanSpeech && strings.TrimSpace(sp.Text) != "" {
			speechCount++
		}
	}
	if speechCount == 0 {
		return Result{}, fmt.Errorf("render: input contains no renderable text")
	}

	// A single-file render is one output, so resume keys on the whole document
	// plus the render-affecting settings. An unchanged output whose file still
	// exists is skipped without synthesizing.
	synthID := synthIdentity(synth)
	key := resumeKey(opts, synthID, text, opts.Out)
	if opts.Resume && !opts.Overwrite {
		if prevM, ok := loadPriorManifest(opts.ManifestPath()); ok {
			if p, found := segmentForOutput(prevM, opts.Out); found && resumable(p, key, opts.Out) {
				return skippedTextResult(opts, prevM), nil
			}
		}
	}

	all, sampleRate, err := synthSpans(ctx, spans, synth, requestMeta(opts, "", opts.Title), opts.Voice, opts.Speed)
	if err != nil {
		return Result{}, err
	}

	if err := writeWAV(opts.Out, sampleRate, all); err != nil {
		return Result{}, fmt.Errorf("render: write %s: %w", opts.Out, err)
	}

	// Manifest segment rows remain speech-only for schema compatibility; silence
	// is reflected only in total duration.
	manifestSegs := make([]ManifestSegment, 0, speechCount)
	for _, sp := range spans {
		if sp.Kind != SpanSpeech || strings.TrimSpace(sp.Text) == "" {
			continue
		}
		manifestSegs = append(manifestSegs, ManifestSegment{
			Index:      len(manifestSegs) + 1,
			ID:         fmt.Sprintf("seg-%03d", len(manifestSegs)+1),
			TextSHA256: textHash(sp.Text),
			ResumeKey:  key,
			Output:     opts.Out,
			DurationMS: 0, // filled below from total after write
			Status:     StatusComplete,
		})
	}
	// Approximate per-speech duration from total samples evenly so tools that
	// sum DurationMS still match wall audio length.
	totalMS := samplesDurationMS(len(all), sampleRate)
	if n := len(manifestSegs); n > 0 {
		each := totalMS / int64(n)
		for i := range manifestSegs {
			manifestSegs[i].DurationMS = each
		}
		// Put remainder on the last segment.
		manifestSegs[n-1].DurationMS += totalMS - each*int64(n)
	}

	var dur time.Duration
	if sampleRate > 0 {
		dur = time.Duration(float64(len(all)) / float64(sampleRate) * float64(time.Second))
	}
	manifest := RenderManifest{
		Schema:       RenderSchema,
		Title:        opts.Title,
		Source:       sourceLabel(opts),
		SourceFormat: opts.ResolveFormat(),
		Voice:        opts.Voice,
		SpeechSpeed:  opts.Speed,
		SampleRate:   sampleRate,
		Segments:     manifestSegs,
	}
	return Result{
		Output:     opts.Out,
		Segments:   len(manifestSegs),
		SampleRate: sampleRate,
		Samples:    len(all),
		Duration:   dur,
		Manifest:   manifest,
	}, nil
}

// segmentForOutput returns the first manifest segment whose output matches.
func segmentForOutput(m RenderManifest, output string) (ManifestSegment, bool) {
	for _, s := range m.Segments {
		if s.Output == output {
			return s, true
		}
	}
	return ManifestSegment{}, false
}

// skippedTextResult builds the result for a single-file render that resume
// skipped: the prior manifest's segments are carried through verbatim (marked
// skipped), so a resumed run reports the same manifest shape, per-segment
// durations, and sample rate as the fresh run it skipped.
func skippedTextResult(opts Options, prevM RenderManifest) Result {
	segs := make([]ManifestSegment, len(prevM.Segments))
	copy(segs, prevM.Segments)
	var totalMS int64
	for i := range segs {
		segs[i].Status = StatusSkipped
		totalMS += segs[i].DurationMS
	}
	manifest := RenderManifest{
		Schema:       RenderSchema,
		Title:        opts.Title,
		Source:       sourceLabel(opts),
		SourceFormat: opts.ResolveFormat(),
		Voice:        opts.Voice,
		SpeechSpeed:  opts.Speed,
		SampleRate:   prevM.SampleRate,
		Segments:     segs,
	}
	return Result{
		Output:     opts.Out,
		Segments:   len(segs),
		SampleRate: prevM.SampleRate,
		Duration:   time.Duration(totalMS) * time.Millisecond,
		Manifest:   manifest,
	}
}

// samplesDurationMS converts a sample count to milliseconds at rate.
func samplesDurationMS(samples, rate int) int64 {
	if rate <= 0 {
		return 0
	}
	return int64(float64(samples) / float64(rate) * 1000)
}

// SegmentText splits text into TTS-sized segments, preserving paragraph
// boundaries and keeping each segment at or under maxChars. A paragraph longer
// than maxChars is split on sentence boundaries (falling back to a hard split).
func SegmentText(text string, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = defaultMaxSegmentChars
	}

	var segs []string
	var buf strings.Builder
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			segs = append(segs, s)
		}
		buf.Reset()
	}

	for _, para := range splitParagraphs(text) {
		para = normalizeWhitespace(para)
		if para == "" {
			continue
		}
		if len(para) > maxChars {
			flush()
			segs = append(segs, splitLong(para, maxChars)...)
			continue
		}
		if buf.Len() > 0 && buf.Len()+1+len(para) > maxChars {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(para)
	}
	flush()
	return segs
}

func splitParagraphs(text string) []string {
	// A blank line (optionally with whitespace) separates paragraphs.
	lines := strings.Split(text, "\n")
	var paras []string
	var cur strings.Builder
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if cur.Len() > 0 {
				paras = append(paras, cur.String())
				cur.Reset()
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		paras = append(paras, cur.String())
	}
	return paras
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// splitLong breaks an over-long paragraph into <=maxChars chunks, preferring a
// sentence boundary near the limit.
func splitLong(p string, maxChars int) []string {
	var chunks []string
	for len(p) > maxChars {
		cut := lastSentenceBreak(p[:maxChars])
		if cut <= 0 {
			// No sentence boundary; hard split at the limit, backed up to a
			// rune boundary so multibyte text (CJK, emoji) is never cut mid-rune
			// into invalid UTF-8.
			cut = maxChars
			for cut > 0 && !utf8.RuneStart(p[cut]) {
				cut--
			}
			if cut == 0 {
				cut = maxChars // degenerate: a single rune longer than the cap
			}
		}
		chunks = append(chunks, strings.TrimSpace(p[:cut]))
		p = strings.TrimSpace(p[cut:])
	}
	if p != "" {
		chunks = append(chunks, p)
	}
	return chunks
}

// lastSentenceBreak returns the index just past the last sentence-ending
// punctuation followed by a space in s, or -1 if none.
func lastSentenceBreak(s string) int {
	best := -1
	for i := 1; i < len(s); i++ {
		if (s[i-1] == '.' || s[i-1] == '!' || s[i-1] == '?') && s[i] == ' ' {
			best = i
		}
	}
	return best
}
