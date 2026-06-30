package render

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// defaultMaxSegmentChars caps the text handed to a single TTS request.
const defaultMaxSegmentChars = 1500

// Synthesizer turns text into mono PCM samples at a sample rate. The command
// layer adapts the cgo TTS provider into this cgo-free interface so render
// orchestration is testable without models.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (samples []float32, sampleRate int, err error)
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
}

// RenderText synthesizes text into a single WAV file at opts.Out. It segments
// the text (paragraph-aware, size-capped), synthesizes each segment, and writes
// the concatenated audio. It performs no STT/brain/microphone/playback work and
// returns promptly on context cancellation.
func RenderText(ctx context.Context, opts Options, text string, synth Synthesizer, writeWAV WAVWriter) (Result, error) {
	if opts.Out == "" {
		return Result{}, fmt.Errorf("render: text rendering requires --out FILE")
	}
	segments := SegmentText(text, defaultMaxSegmentChars)
	if len(segments) == 0 {
		return Result{}, fmt.Errorf("render: input contains no renderable text")
	}

	var all []float32
	sampleRate := 0
	for i, seg := range segments {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		samples, rate, err := synth.Synthesize(ctx, seg)
		if err != nil {
			return Result{}, fmt.Errorf("render: synthesize segment %d: %w", i+1, err)
		}
		if sampleRate == 0 {
			sampleRate = rate
		}
		all = append(all, samples...)
	}

	if err := writeWAV(opts.Out, sampleRate, all); err != nil {
		return Result{}, fmt.Errorf("render: write %s: %w", opts.Out, err)
	}

	var dur time.Duration
	if sampleRate > 0 {
		dur = time.Duration(float64(len(all)) / float64(sampleRate) * float64(time.Second))
	}
	return Result{
		Output:     opts.Out,
		Segments:   len(segments),
		SampleRate: sampleRate,
		Samples:    len(all),
		Duration:   dur,
	}, nil
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
			cut = maxChars // no sentence boundary; hard split
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
