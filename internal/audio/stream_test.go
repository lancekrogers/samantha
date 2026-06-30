package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamWriteUnblocksOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewPCMStream(ctx)
	if err := s.SetSampleRate(24000); err != nil {
		t.Fatalf("SetSampleRate() error = %v", err)
	}

	// Fill the frames buffer so the next Write has to block.
	for i := 0; i < cap(s.frames); i++ {
		if err := s.Write([]float32{0.1}); err != nil {
			t.Fatalf("Write(%d) error = %v", i, err)
		}
	}

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- s.Write([]float32{0.2})
	}()

	select {
	case err := <-writeErr:
		t.Fatalf("blocked Write returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-writeErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Write() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Write did not return after context cancel")
	}
}
