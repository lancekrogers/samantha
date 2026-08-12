package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngressWriteReadFinalize(t *testing.T) {
	ing := NewIngress()
	if err := ing.Write(make([]float32, 160)); err != nil {
		t.Fatal(err)
	}
	frame, err := ing.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) != 160 {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	ing.Finalize()
	frame, err = ing.ReadFrame(context.Background())
	if err != nil || !frame.Final {
		t.Fatalf("want Final frame, got %+v err=%v", frame, err)
	}
}

func TestIngressReadFrameCancels(t *testing.T) {
	ing := NewIngress()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := ing.ReadFrame(ctx)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context error", err)
	}
}

func TestIngressResetAllowsNextUtterance(t *testing.T) {
	ing := NewIngress()
	_ = ing.Write([]float32{0.1, 0.2})
	ing.Finalize()
	_, _ = ing.ReadFrame(context.Background()) // samples
	_, _ = ing.ReadFrame(context.Background()) // final
	ing.Reset()
	_ = ing.Write([]float32{0.3})
	frame, err := ing.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) != 1 {
		t.Fatalf("after reset: %+v err=%v", frame, err)
	}
}
