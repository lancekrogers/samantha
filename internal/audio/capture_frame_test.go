package audio

import (
	"context"
	"errors"
	"testing"
)

func TestCaptureReadFrameNoFrameReadyThenLive(t *testing.T) {
	c := NewCapture() // ring buffer only; no device started
	ctx := context.Background()

	// Empty buffer reports no-frame-ready, NOT end-of-input.
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Fatalf("ReadFrame() on empty capture = %v, want ErrNoFrameReady", err)
	}

	// Feed a chunk as the device callback would.
	c.buf.Write(make([]float32, ChunkSize))
	f, err := c.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() with buffered audio error = %v", err)
	}
	if f.Final {
		t.Error("live frame Final = true, want false (live never finalizes on silence)")
	}
	if f.SourceKind != SourceLive {
		t.Errorf("live frame SourceKind = %q, want %q", f.SourceKind, SourceLive)
	}
	if len(f.Samples) != ChunkSize {
		t.Errorf("live frame samples = %d, want %d", len(f.Samples), ChunkSize)
	}
	if f.Sequence != 1 {
		t.Errorf("live frame Sequence = %d, want 1", f.Sequence)
	}

	// Drained again: back to no-frame-ready, still never Final.
	if _, err := c.ReadFrame(ctx); !errors.Is(err, ErrNoFrameReady) {
		t.Errorf("drained capture = %v, want ErrNoFrameReady", err)
	}
}

func TestCaptureReadFrameCancellation(t *testing.T) {
	c := NewCapture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFrame() on canceled ctx = %v, want context.Canceled", err)
	}
}

func TestCaptureCloseIsSafeWhenNotRunning(t *testing.T) {
	c := NewCapture()
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}
