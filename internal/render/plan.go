package render

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"
)

// SpanKind identifies a planned render span.
type SpanKind string

const (
	SpanSpeech  SpanKind = "speech"
	SpanSilence SpanKind = "silence"
)

// RenderSpan is one planned audio span: either speech text or a silence gap.
type RenderSpan struct {
	Kind     SpanKind
	Text     string
	Duration time.Duration
	SourceID string
}

// PlanSpeechSpans segments text into speech spans using the effective segment
// cap. Pause options are applied by PlanUnitSpans / PlanTextSpans.
func PlanSpeechSpans(text string, maxChars int) []RenderSpan {
	segs := SegmentText(text, maxChars)
	spans := make([]RenderSpan, 0, len(segs))
	for _, s := range segs {
		spans = append(spans, RenderSpan{Kind: SpanSpeech, Text: s})
	}
	return spans
}

// PlanTextSpans builds the full span plan for a single-file render.
func PlanTextSpans(opts Options, text string) []RenderSpan {
	return planWithPauses(opts, text, false)
}

// PlanUnitSpans builds the span plan for one multi-file unit. When heading
// pause is set, silence is inserted after the first paragraph (typically the
// section title in unit narration).
func PlanUnitSpans(opts Options, text string) []RenderSpan {
	return planWithPauses(opts, text, true)
}

func planWithPauses(opts Options, text string, unit bool) []RenderSpan {
	headingPause := opts.PauseHeadingDuration()
	paraPause := opts.PauseParagraphDuration()
	maxChars := opts.EffectiveMaxSegmentChars()

	if headingPause == 0 && paraPause == 0 {
		return PlanSpeechSpans(text, maxChars)
	}

	paras := splitParagraphs(text)
	var spans []RenderSpan
	speechSeen := false
	firstIdx := firstNonEmptyParaIndex(paras)
	for i, para := range paras {
		para = normalizeWhitespace(para)
		if para == "" {
			continue
		}
		speech := PlanSpeechSpans(para, maxChars)
		if len(speech) == 0 {
			continue
		}
		spans = append(spans, speech...)
		speechSeen = true

		isLast := true
		for j := i + 1; j < len(paras); j++ {
			if normalizeWhitespace(paras[j]) != "" {
				isLast = false
				break
			}
		}
		if isLast {
			continue
		}

		// First paragraph of a unit is typically the heading title.
		if unit && i == firstIdx && headingPause > 0 {
			spans = append(spans, RenderSpan{Kind: SpanSilence, Duration: headingPause})
			continue
		}
		if paraPause > 0 {
			spans = append(spans, RenderSpan{Kind: SpanSilence, Duration: paraPause})
		}
	}
	if !speechSeen {
		return nil
	}
	return spans
}

func firstNonEmptyParaIndex(paras []string) int {
	for i, p := range paras {
		if normalizeWhitespace(p) != "" {
			return i
		}
	}
	return 0
}

// synthSpans synthesizes speech spans and inserts zero samples for silence.
// Silence before the first speech sample is buffered until the sample rate is
// known. Plans with only silence return an error. meta is attached to each
// speech request when the synthesizer implements RequestSynthesizer.
func synthSpans(ctx context.Context, spans []RenderSpan, synth Synthesizer, meta map[string]string, voice string, speed float64) ([]float32, int, error) {
	var all []float32
	rate := 0
	var pendingSilence time.Duration
	speechCount := 0
	reqSynth, useReq := synth.(RequestSynthesizer)

	for _, sp := range spans {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		switch sp.Kind {
		case SpanSpeech:
			if strings.TrimSpace(sp.Text) == "" {
				continue
			}
			var samples []float32
			var r int
			var err error
			if useReq {
				m := cloneMeta(meta)
				m["segment_index"] = fmt.Sprintf("%d", speechCount+1)
				samples, r, err = reqSynth.SynthesizeRequest(ctx, SynthesisRequest{
					Text:     sp.Text,
					Voice:    voice,
					Speed:    speed,
					Metadata: m,
				})
			} else {
				samples, r, err = synth.Synthesize(ctx, sp.Text)
			}
			if err != nil {
				return nil, 0, fmt.Errorf("synthesize segment %d: %w", speechCount+1, err)
			}
			if rate == 0 {
				rate = r
				if pendingSilence > 0 && rate > 0 {
					all = append(all, silenceSamples(pendingSilence, rate)...)
					pendingSilence = 0
				}
			}
			all = append(all, samples...)
			speechCount++
		case SpanSilence:
			if sp.Duration <= 0 {
				continue
			}
			if rate == 0 {
				pendingSilence += sp.Duration
				continue
			}
			all = append(all, silenceSamples(sp.Duration, rate)...)
		}
	}
	if speechCount == 0 {
		return nil, 0, fmt.Errorf("render: input contains no renderable text")
	}
	return all, rate, nil
}

func cloneMeta(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	maps.Copy(out, in)
	return out
}

// requestMeta builds stable synthesis metadata for batch render requests.
func requestMeta(opts Options, unitID, unitTitle string) map[string]string {
	m := map[string]string{
		"source":        sourceLabel(opts),
		"source_format": string(opts.ResolveFormat()),
		"title":         opts.Title,
		"voice":         opts.Voice,
		"speed":         fmt.Sprintf("%g", opts.Speed),
	}
	if unitID != "" {
		m["unit_id"] = unitID
	}
	if unitTitle != "" {
		m["unit_title"] = unitTitle
	}
	return m
}

func silenceSamples(d time.Duration, rate int) []float32 {
	if d <= 0 || rate <= 0 {
		return nil
	}
	n := int(d.Seconds() * float64(rate))
	if n <= 0 {
		return nil
	}
	return make([]float32, n)
}
