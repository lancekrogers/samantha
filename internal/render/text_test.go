package render

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeSynth produces deterministic samples: one sample per character, at a fixed
// rate, recording the segments it was asked to synthesize.
type fakeSynth struct {
	rate  int
	calls []string
	err   error
}

func (f *fakeSynth) Synthesize(ctx context.Context, text string) ([]float32, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.calls = append(f.calls, text)
	return make([]float32, len(text)), f.rate, nil
}

func TestSegmentTextPreservesParagraphsUnderCap(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph here.\n\n\nThird."
	segs := SegmentText(text, 1000)
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1 (all paragraphs fit under the cap)\n%v", len(segs), segs)
	}
	for _, p := range []string{"First paragraph.", "Second paragraph here.", "Third."} {
		if !strings.Contains(segs[0], p) {
			t.Errorf("segment missing %q: %q", p, segs[0])
		}
	}
}

func TestSegmentTextSplitsByCap(t *testing.T) {
	text := "Para one is here.\n\nPara two is here.\n\nPara three is here."
	segs := SegmentText(text, 20) // each paragraph ~17 chars, so one per segment
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3\n%v", len(segs), segs)
	}
}

func TestSegmentTextHardSplitsMultibyteOnRuneBoundary(t *testing.T) {
	// A long paragraph of multibyte runes with no ASCII sentence break must be
	// hard-split on rune boundaries, never mid-rune into invalid UTF-8.
	text := strings.Repeat("あ", 800) // 2400 bytes, no ". " break
	segs := SegmentText(text, 1501)
	if len(segs) < 2 {
		t.Fatalf("expected the over-long paragraph to split, got %d segment(s)", len(segs))
	}
	for i, s := range segs {
		if !utf8.ValidString(s) {
			t.Errorf("segment %d is not valid UTF-8 (cut mid-rune): %x", i, s)
		}
	}
}

func TestSegmentTextSplitsLongParagraphAtSentences(t *testing.T) {
	text := "Sentence one is reasonably sized. Sentence two is also here. Sentence three closes it out."
	segs := SegmentText(text, 40)
	if len(segs) < 2 {
		t.Fatalf("long paragraph should split into multiple segments, got %d", len(segs))
	}
	for _, s := range segs {
		if len(s) > 40 && !strings.Contains(s, " ") {
			t.Errorf("segment exceeds cap without a break point: %q", s)
		}
	}
}

func TestRenderTextWritesConcatenatedWAV(t *testing.T) {
	var wrotePath string
	var wroteSamples int
	var wroteRate int
	writeWAV := func(path string, rate int, samples []float32) error {
		wrotePath, wroteRate, wroteSamples = path, rate, len(samples)
		return nil
	}

	synth := &fakeSynth{rate: 24000}
	opts := Options{Stdin: true, Out: "out.wav", Format: FormatText}
	text := "Alpha paragraph.\n\nBeta paragraph."

	result, err := RenderText(context.Background(), opts, text, synth, writeWAV)
	if err != nil {
		t.Fatalf("RenderText() error = %v", err)
	}
	if wrotePath != "out.wav" || wroteRate != 24000 {
		t.Fatalf("wrote path=%q rate=%d, want out.wav/24000", wrotePath, wroteRate)
	}
	if wroteSamples == 0 || result.Samples != wroteSamples {
		t.Fatalf("samples written = %d, result = %d, want > 0 and equal", wroteSamples, result.Samples)
	}
	if result.SampleRate != 24000 || result.Segments == 0 {
		t.Fatalf("result = %+v, want rate 24000 and >0 segments", result)
	}
}

func TestRenderTextEmptyInputFails(t *testing.T) {
	writeWAV := func(string, int, []float32) error { return nil }
	_, err := RenderText(context.Background(), Options{Out: "x.wav"}, "   \n\n  ", &fakeSynth{rate: 24000}, writeWAV)
	if err == nil || !strings.Contains(err.Error(), "no renderable text") {
		t.Fatalf("error = %v, want an empty-input error", err)
	}
}

func TestRenderTextSynthesisErrorNamesSegment(t *testing.T) {
	writeWAV := func(string, int, []float32) error { return nil }
	synth := &fakeSynth{rate: 24000, err: errors.New("kokoro failed")}
	_, err := RenderText(context.Background(), Options{Out: "x.wav"}, "hello", synth, writeWAV)
	if err == nil || !strings.Contains(err.Error(), "segment 1") {
		t.Fatalf("error = %v, want it to name the failing segment", err)
	}
}

func TestRenderTextCancellationReturnsPromptly(t *testing.T) {
	writeWAV := func(string, int, []float32) error {
		t.Error("writeWAV should not be called after cancellation")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RenderText(ctx, Options{Out: "x.wav"}, "a\n\nb\n\nc", &fakeSynth{rate: 24000}, writeWAV)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
