package render

import (
	"context"
	"testing"
	"time"
)

func TestPlanSpeechSpansMatchesSegmentText(t *testing.T) {
	text := "Hello world.\n\nSecond paragraph."
	segs := SegmentText(text, 1500)
	spans := PlanSpeechSpans(text, 1500)
	if len(spans) != len(segs) {
		t.Fatalf("spans=%d segs=%d", len(spans), len(segs))
	}
	for i := range segs {
		if spans[i].Kind != SpanSpeech || spans[i].Text != segs[i] {
			t.Errorf("span[%d]=%+v want speech %q", i, spans[i], segs[i])
		}
	}
}

func TestPlanTextSpansEmpty(t *testing.T) {
	if got := PlanTextSpans(Options{}, "   \n\n"); len(got) != 0 {
		t.Errorf("empty plan = %+v", got)
	}
}

func TestPlanUnitSpansInsertsHeadingAndParagraphPauses(t *testing.T) {
	opts := Options{PauseHeading: "1s", PauseParagraph: "500ms"}
	text := "Title\n\nBody one.\n\nBody two."
	spans := PlanUnitSpans(opts, text)
	var silences []time.Duration
	var speech int
	for _, sp := range spans {
		switch sp.Kind {
		case SpanSpeech:
			speech++
		case SpanSilence:
			silences = append(silences, sp.Duration)
		}
	}
	if speech < 2 {
		t.Fatalf("speech spans = %d, want >= 2: %+v", speech, spans)
	}
	if len(silences) != 2 || silences[0] != time.Second || silences[1] != 500*time.Millisecond {
		t.Fatalf("silences = %v, want [1s 500ms]", silences)
	}
}

func TestSynthSpansAddsSilenceDuration(t *testing.T) {
	spans := []RenderSpan{
		{Kind: SpanSpeech, Text: "hi"}, // fakeSynth: 2 samples
		{Kind: SpanSilence, Duration: time.Second},
	}
	samples, rate, err := synthSpans(context.Background(), spans, &fakeSynth{rate: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 2 speech samples + 10 silence samples at rate 10 for 1s
	if rate != 10 || len(samples) != 12 {
		t.Fatalf("samples=%d rate=%d, want 12 @ 10", len(samples), rate)
	}
}

func TestResumeKeyIncludesSegmentCapAndPauses(t *testing.T) {
	base := Options{Input: "a.md", Out: "x.wav", Format: FormatMarkdown, Voice: "v", Speed: 1}
	k1 := resumeKey(base, "s", "text", "x.wav")
	withCap := base
	withCap.MaxSegmentChars = 200
	if resumeKey(withCap, "s", "text", "x.wav") == k1 {
		t.Error("max-segment-chars should change resume key")
	}
	withPause := base
	withPause.PauseHeading = "1s"
	if resumeKey(withPause, "s", "text", "x.wav") == k1 {
		t.Error("pause-heading should change resume key")
	}
	withCode := base
	withCode.CodeBlocks = "read"
	if resumeKey(withCode, "s", "text", "x.wav") == k1 {
		t.Error("code-blocks should change resume key")
	}
}
