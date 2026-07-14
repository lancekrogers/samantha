package stt

import (
	"testing"
)

func TestPreRollSamplesFromMS(t *testing.T) {
	cases := []struct {
		ms   int
		want int
	}{
		{0, 0},
		{-50, 0},
		{300, 4800}, // 300ms at 16kHz
		{100, 1600},
	}
	for _, c := range cases {
		if got := preRollSamplesFromMS(c.ms); got != c.want {
			t.Errorf("preRollSamplesFromMS(%d) = %d, want %d", c.ms, got, c.want)
		}
	}
}

// fakeVAD is a deterministic vadDetector double: it emits one queued segment at
// a caller-chosen start offset so the pre-roll splice can be tested without cgo.
type fakeVAD struct {
	segSamples []float32
	segStart   int
	hasSeg     bool
}

func (f *fakeVAD) AcceptWaveform([]float32) {}
func (f *fakeVAD) IsSpeech() bool           { return false }
func (f *fakeVAD) IsSpeechDetected() bool   { return f.hasSeg }
func (f *fakeVAD) IsEmpty() bool            { return !f.hasSeg }
func (f *fakeVAD) FrontSegment() ([]float32, int) {
	return f.segSamples, f.segStart
}
func (f *fakeVAD) Pop()   { f.hasSeg = false }
func (f *fakeVAD) Clear() {}
func (f *fakeVAD) Flush() {}

// ramp returns n samples counting up from start*0.001 so buffers are visually
// distinct and order-sensitive assertions are meaningful.
func ramp(startIdx, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(startIdx+i) * 0.001
	}
	return out
}

func floatsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVADSegmenterPrependsOnsetPreRoll(t *testing.T) {
	fake := &fakeVAD{}
	// preRoll = 4 samples. Feed 10 samples of continuous audio (indices 0..9);
	// the VAD "detects" a segment starting at index 6 (samples 6..9), having
	// trimmed the 0..5 onset. Pre-roll should recover indices 2..5.
	seg := newVADSegmenter(fake, 4)
	all := ramp(0, 10)
	seg.AcceptWaveform(all)

	fake.segSamples = all[6:10]
	fake.segStart = 6
	fake.hasSeg = true

	got, ok := seg.NextSegment()
	if !ok {
		t.Fatal("NextSegment() returned ok=false")
	}
	want := all[2:10] // 4 pre-roll samples (2..5) + segment (6..9)
	if !floatsEqual(got, want) {
		t.Fatalf("NextSegment() = %v, want %v", got, want)
	}
}

func TestVADSegmenterPreRollDisabledReturnsSegmentUnchanged(t *testing.T) {
	fake := &fakeVAD{}
	seg := newVADSegmenter(fake, 0)
	all := ramp(0, 10)
	seg.AcceptWaveform(all)

	fake.segSamples = all[6:10]
	fake.segStart = 6
	fake.hasSeg = true

	got, ok := seg.NextSegment()
	if !ok || !floatsEqual(got, all[6:10]) {
		t.Fatalf("NextSegment() = %v (ok=%v), want unchanged segment %v", got, ok, all[6:10])
	}
}

func TestVADSegmenterClampsPreRollToAvailableWindow(t *testing.T) {
	fake := &fakeVAD{}
	// preRoll wants 4 samples but the segment starts at index 2, so only 2
	// pre-roll samples (0..1) are available — no under-run past the window base.
	seg := newVADSegmenter(fake, 4)
	all := ramp(0, 8)
	seg.AcceptWaveform(all)

	fake.segSamples = all[2:8]
	fake.segStart = 2
	fake.hasSeg = true

	got, ok := seg.NextSegment()
	if !ok {
		t.Fatal("NextSegment() returned ok=false")
	}
	if !floatsEqual(got, all[0:8]) {
		t.Fatalf("NextSegment() = %v, want %v", got, all[0:8])
	}
}

func TestVADSegmenterSkipsSpliceWhenStartOutsideWindow(t *testing.T) {
	fake := &fakeVAD{}
	// A start offset beyond what was fed (misaligned basis, e.g. detector did
	// not share the reset) must degrade to the unmodified segment, never splice
	// arbitrary audio.
	seg := newVADSegmenter(fake, 4)
	seg.AcceptWaveform(ramp(0, 8))

	orphan := ramp(100, 3)
	fake.segSamples = orphan
	fake.segStart = 9999
	fake.hasSeg = true

	got, ok := seg.NextSegment()
	if !ok || !floatsEqual(got, orphan) {
		t.Fatalf("NextSegment() = %v (ok=%v), want unchanged %v", got, ok, orphan)
	}
}

func TestVADSegmenterResetRealignsOffsets(t *testing.T) {
	fake := &fakeVAD{}
	seg := newVADSegmenter(fake, 4)
	seg.AcceptWaveform(ramp(0, 100)) // stale audio from a previous turn

	seg.Reset()
	if seg.fed != 0 || len(seg.window) != 0 || seg.windowBase != 0 {
		t.Fatalf("Reset did not clear pre-roll state: fed=%d window=%d base=%d", seg.fed, len(seg.window), seg.windowBase)
	}

	// New turn: offsets restart at 0, so a segment at index 6 splices the fresh
	// pre-roll, not the stale pre-Reset audio.
	fresh := ramp(500, 10)
	seg.AcceptWaveform(fresh)
	fake.segSamples = fresh[6:10]
	fake.segStart = 6
	fake.hasSeg = true

	got, ok := seg.NextSegment()
	if !ok || !floatsEqual(got, fresh[2:10]) {
		t.Fatalf("NextSegment() = %v (ok=%v), want %v", got, ok, fresh[2:10])
	}
}
