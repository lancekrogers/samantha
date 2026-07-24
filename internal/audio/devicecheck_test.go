package audio

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
)

func TestDeviceCheckerEnumerateError(t *testing.T) {
	wantErr := errors.New("backend broken")
	c := &DeviceChecker{enumerate: func(malgo.DeviceType) ([]string, error) { return nil, wantErr }}

	if _, err := c.CaptureDevices(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("CaptureDevices error = %v, want %v", err, wantErr)
	}
	if _, err := c.PlaybackDevices(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("PlaybackDevices error = %v, want %v", err, wantErr)
	}
}

func TestDeviceCheckerCancelledContext(t *testing.T) {
	c := &DeviceChecker{enumerate: func(malgo.DeviceType) ([]string, error) {
		t.Error("enumerate must not run with a cancelled context")
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.CaptureDevices(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("CaptureDevices error = %v, want context.Canceled", err)
	}
}

func TestDeviceCheckerTimeoutOnWedgedBackend(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	c := &DeviceChecker{enumerate: func(malgo.DeviceType) ([]string, error) {
		<-block
		return nil, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := c.CaptureDevices(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CaptureDevices error = %v, want context.DeadlineExceeded", err)
	}
}

func TestDeviceCheckerReturnsNames(t *testing.T) {
	c := &DeviceChecker{enumerate: func(kind malgo.DeviceType) ([]string, error) {
		if kind == malgo.Capture {
			return []string{"Built-in Mic"}, nil
		}
		return []string{"Built-in Speaker", "Headphones"}, nil
	}}

	mics, err := c.CaptureDevices(context.Background())
	if err != nil || len(mics) != 1 || mics[0] != "Built-in Mic" {
		t.Errorf("CaptureDevices = %v, %v", mics, err)
	}
	speakers, err := c.PlaybackDevices(context.Background())
	if err != nil || len(speakers) != 2 {
		t.Errorf("PlaybackDevices = %v, %v", speakers, err)
	}
}

func TestCopyDeviceIDToCUsesDetachedStorage(t *testing.T) {
	var id malgo.DeviceID
	for i := range id {
		id[i] = byte(i)
	}

	ptr, release := copyDeviceIDToC(id)
	t.Cleanup(release)
	if ptr == nil {
		t.Fatal("copyDeviceIDToC returned a nil pointer")
	}
	if ptr == unsafe.Pointer(&id[0]) {
		t.Fatal("copyDeviceIDToC retained Go-managed DeviceID storage")
	}

	got := *(*malgo.DeviceID)(ptr)
	if got != id {
		t.Fatalf("copied device ID differs from source")
	}

	id[0] ^= 0xff
	if copied := *(*malgo.DeviceID)(ptr); copied == id {
		t.Fatal("C-owned device ID changed when the Go source was mutated")
	}

	// Release is intentionally idempotent because several device-error paths
	// converge on deferred cleanup.
	release()
	release()
}
