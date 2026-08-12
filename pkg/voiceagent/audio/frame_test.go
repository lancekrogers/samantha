package audio

import (
	"context"
	"errors"
	"testing"
)

// fixtureFrames is a finite reference FrameSource: it emits each chunk as a
// frame, then one Final frame, then ErrSourceClosed. It proves the finite
// end-of-input contract.
type fixtureFrames struct {
	chunks [][]float32
	i      int
	closed bool
}

func (s *fixtureFrames) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if s.closed {
		return Frame{}, ErrSourceClosed
	}
	if s.i >= len(s.chunks) {
		s.closed = true
		return Frame{SourceKind: SourceFixture, Final: true}, nil
	}
	f := Frame{
		Samples:    s.chunks[s.i],
		SampleRate: SampleRate,
		Channels:   Channels,
		Sequence:   int64(s.i + 1),
		SourceKind: SourceFixture,
	}
	s.i++
	return f, nil
}

func (s *fixtureFrames) Close() error { s.closed = true; return nil }

// liveFrames is a live reference FrameSource: it returns ErrNoFrameReady until
// frames are queued, never sets Final, and stops on cancellation.
type liveFrames struct {
	queue [][]float32
	seq   int64
}

func (s *liveFrames) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if len(s.queue) == 0 {
		return Frame{}, ErrNoFrameReady
	}
	chunk := s.queue[0]
	s.queue = s.queue[1:]
	s.seq++
	return Frame{
		Samples:    chunk,
		SampleRate: SampleRate,
		Channels:   Channels,
		Sequence:   s.seq,
		SourceKind: SourceLive,
	}, nil
}

func (s *liveFrames) Close() error { return nil }

// Compile-time proof the reference sources satisfy the contract.
var (
	_ FrameSource = (*fixtureFrames)(nil)
	_ FrameSource = (*liveFrames)(nil)
)

func TestFrameEmpty(t *testing.T) {
	if !(Frame{}).Empty() {
		t.Error("zero Frame.Empty() = false, want true")
	}
	if (Frame{Samples: []float32{0.1}}).Empty() {
		t.Error("Frame with samples Empty() = true, want false")
	}
}

func TestFiniteSourceSignalsEOFThenClosed(t *testing.T) {
	src := &fixtureFrames{chunks: [][]float32{{0.1, 0.2}, {0.3, 0.4}}}
	ctx := context.Background()

	for want := 1; want <= 2; want++ {
		f, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame() error = %v, want nil", err)
		}
		if f.Final {
			t.Fatalf("frame %d Final = true, want false", want)
		}
		if f.Empty() {
			t.Fatalf("frame %d is empty, want samples", want)
		}
		if f.SourceKind != SourceFixture {
			t.Fatalf("frame %d SourceKind = %q, want %q", want, f.SourceKind, SourceFixture)
		}
		if f.Sequence != int64(want) {
			t.Fatalf("frame %d Sequence = %d, want %d", want, f.Sequence, want)
		}
	}

	final, err := src.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() at EOF error = %v, want nil", err)
	}
	if !final.Final {
		t.Error("EOF frame Final = false, want true")
	}
	if !final.Empty() {
		t.Error("EOF frame carries samples, want empty")
	}

	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrSourceClosed) {
		t.Errorf("ReadFrame() after EOF error = %v, want ErrSourceClosed", err)
	}
}

func TestSourceReturnsCancellationPromptly(t *testing.T) {
	src := &fixtureFrames{chunks: [][]float32{{0.1}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := src.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFrame() on canceled ctx error = %v, want context.Canceled", err)
	}
}

func TestLiveSourceReportsNoFrameReady(t *testing.T) {
	src := &liveFrames{}
	ctx := context.Background()

	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame() on empty live source error = %v, want ErrNoFrameReady", err)
	}

	src.queue = append(src.queue, []float32{0.5})
	f, err := src.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() with queued audio error = %v, want nil", err)
	}
	if f.Final {
		t.Error("live frame Final = true, want false (live sources never finalize)")
	}
	if f.SourceKind != SourceLive {
		t.Errorf("live frame SourceKind = %q, want %q", f.SourceKind, SourceLive)
	}

	// Drained again: back to no-frame-ready, still never Final.
	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Errorf("drained live source error = %v, want ErrNoFrameReady", err)
	}
}
