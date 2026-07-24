package audio

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWAVWriterStreamsAndStaysReadableBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.wav")
	w, err := NewWAVWriter(path, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]float32{0.5, -0.5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]float32{0.25}); err != nil {
		t.Fatal(err)
	}
	if w.Samples() != 3 {
		t.Fatalf("Samples() = %d, want 3", w.Samples())
	}

	// Crash resilience: the file is a valid WAV up to the last flushed chunk
	// even though Close has not run yet.
	mid, rate, err := ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("mid-stream read: %v", err)
	}
	if rate != SampleRate || len(mid) != 3 {
		t.Fatalf("mid-stream = %d samples @ %d Hz, want 3 @ %d", len(mid), rate, SampleRate)
	}
	if math.Abs(float64(mid[0])-0.5) > 0.001 {
		t.Fatalf("mid[0] = %v, want ~0.5", mid[0])
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	final, _, err := ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("post-close read: %v", err)
	}
	if len(final) != 3 {
		t.Fatalf("post-close = %d samples, want 3", len(final))
	}

	// A newly created writer must be 0600 (private meeting audio).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("wav mode = %o, want private", info.Mode().Perm())
	}
}

func TestWAVWriterMatchesOneShotBytes(t *testing.T) {
	dir := t.TempDir()
	samples := []float32{0, 0.1, -0.2, 0.9, -1.0, 1.0}

	oneShot := filepath.Join(dir, "oneshot.wav")
	if err := WriteWAVFloat32(oneShot, SampleRate, samples); err != nil {
		t.Fatal(err)
	}
	streamed := filepath.Join(dir, "streamed.wav")
	w, err := NewWAVWriter(streamed, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	// Split across two writes to exercise incremental appends.
	if err := w.Write(samples[:2]); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(samples[2:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	a, err := os.ReadFile(oneShot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(streamed)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("byte lengths differ: one-shot %d vs streamed %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("byte %d differs: one-shot %#x vs streamed %#x", i, a[i], b[i])
		}
	}
}

func TestWAVWriterWriteAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed.wav")
	w, err := NewWAVWriter(path, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]float32{0.1}); err == nil {
		t.Fatal("write after close should fail")
	}
	// Close is idempotent.
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
