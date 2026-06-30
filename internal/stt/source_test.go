package stt

import (
	"context"
	"errors"
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
)

// liveOnly implements only the legacy audioSource (no Exhausted), like live capture.
type liveOnly struct{}

func (liveOnly) Read() []float32 { return nil }

// finiteSrc implements the finite contract, like a fixture.
type finiteSrc struct{ done bool }

func (f *finiteSrc) Read() []float32 { return nil }
func (f *finiteSrc) Exhausted() bool { return f.done }

func TestSourceKindClassifiesLiveVsFinite(t *testing.T) {
	if got := sourceKind(liveOnly{}); got != audio.SourceLive {
		t.Errorf("sourceKind(live) = %q, want %q", got, audio.SourceLive)
	}
	if got := sourceKind(&finiteSrc{}); got != audio.SourceFixture {
		t.Errorf("sourceKind(finite) = %q, want %q", got, audio.SourceFixture)
	}
}

func TestSourceExhausted(t *testing.T) {
	if sourceExhausted(liveOnly{}) {
		t.Error("sourceExhausted(live) = true, want false (live never exhausts)")
	}

	f := &finiteSrc{done: false}
	if sourceExhausted(f) {
		t.Error("sourceExhausted(not-done) = true, want false")
	}
	f.done = true
	if !sourceExhausted(f) {
		t.Error("sourceExhausted(done) = false, want true")
	}
}

// scriptedLegacy is a legacy finite audioSource that returns chunks then exhausts.
type scriptedLegacy struct {
	chunks [][]float32
	i      int
}

func (s *scriptedLegacy) Read() []float32 {
	if s.i >= len(s.chunks) {
		return nil
	}
	c := s.chunks[s.i]
	s.i++
	return c
}

func (s *scriptedLegacy) Exhausted() bool { return s.i >= len(s.chunks) }

func TestLegacyFrameSourceFiniteReportsFinalEOF(t *testing.T) {
	src := newLegacyFrameSource(&scriptedLegacy{chunks: [][]float32{{0.1}, {0.2}}})
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		f, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame() frame %d error = %v", i, err)
		}
		if f.Final || f.Empty() {
			t.Fatalf("frame %d: want data frame, got final=%v empty=%v", i, f.Final, f.Empty())
		}
		if f.SourceKind != audio.SourceFixture {
			t.Errorf("frame %d SourceKind = %q, want fixture", i, f.SourceKind)
		}
	}

	f, err := src.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() at EOF error = %v", err)
	}
	if !f.Final {
		t.Error("want Final frame at EOF")
	}
}

func TestLegacyFrameSourceLiveReportsNoFrameReady(t *testing.T) {
	// liveOnly.Read() returns nil and the source is not finite → no-frame-ready,
	// never EOF.
	src := newLegacyFrameSource(liveOnly{})
	if _, err := src.ReadFrame(context.Background()); !errors.Is(err, audio.ErrNoFrameReady) {
		t.Errorf("live empty read = %v, want ErrNoFrameReady", err)
	}
}

func TestLegacyFrameSourceCancellation(t *testing.T) {
	src := newLegacyFrameSource(liveOnly{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled ReadFrame = %v, want context.Canceled", err)
	}
}
