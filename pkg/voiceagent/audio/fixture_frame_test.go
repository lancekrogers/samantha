package audio

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFixtureReadFrameEmitsFramesThenFinal(t *testing.T) {
	// 2400 samples at ChunkSize 1600 -> two chunks (1600 + 800), then Final.
	src := &FixtureSource{chunks: ChunkSamples(make([]float32, 2400), ChunkSize)}
	ctx := context.Background()

	frames := 0
	for {
		f, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		if f.Final {
			if !f.Empty() {
				t.Error("Final frame carried samples, want empty terminal frame")
			}
			break
		}
		frames++
		if f.SourceKind != SourceFixture {
			t.Errorf("frame %d SourceKind = %q, want %q", frames, f.SourceKind, SourceFixture)
		}
		if f.Sequence != int64(frames) {
			t.Errorf("frame Sequence = %d, want %d", f.Sequence, frames)
		}
		if f.Empty() {
			t.Errorf("frame %d empty, want samples", frames)
		}
	}
	if frames != 2 {
		t.Fatalf("emitted %d frames before Final, want 2", frames)
	}

	if _, err := src.ReadFrame(ctx); !errors.Is(err, ErrSourceClosed) {
		t.Errorf("ReadFrame() after Final = %v, want ErrSourceClosed", err)
	}
}

// TestShortFixtureReachesEOFWithoutRealtime is the regression guard: a short
// (sub-second) fixture must reach an explicit Final frame from its own EOF, with
// real-time pacing disabled, so transcription no longer depends on silence timing.
func TestShortFixtureReachesEOFWithoutRealtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.wav")
	samples := make([]float32, ChunkSize+400) // just over one chunk
	for i := range samples {
		samples[i] = 0.2
	}
	if err := WriteWAVFloat32(path, SampleRate, samples); err != nil {
		t.Fatal(err)
	}

	src, err := NewFixtureSourceFromWAV(path, ChunkSize, false) // realtime=false
	if err != nil {
		t.Fatalf("NewFixtureSourceFromWAV() error = %v", err)
	}

	sawData, sawFinal := false, false
	for i := 0; i < 10 && !sawFinal; i++ {
		f, err := src.ReadFrame(context.Background())
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		if f.Final {
			sawFinal = true
		} else if !f.Empty() {
			sawData = true
		}
	}
	if !sawData {
		t.Error("short fixture produced no data frames")
	}
	if !sawFinal {
		t.Fatal("short fixture never produced a Final frame")
	}
}

func TestFixtureReadFrameCancellation(t *testing.T) {
	src := &FixtureSource{chunks: ChunkSamples(make([]float32, ChunkSize), ChunkSize)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := src.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFrame() on canceled ctx = %v, want context.Canceled", err)
	}
}

func TestFixtureCloseStopsReads(t *testing.T) {
	src := &FixtureSource{chunks: ChunkSamples(make([]float32, ChunkSize), ChunkSize)}
	if err := src.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := src.ReadFrame(context.Background()); !errors.Is(err, ErrSourceClosed) {
		t.Errorf("ReadFrame() after Close = %v, want ErrSourceClosed", err)
	}
}

func TestFixtureResetReplaysFromStart(t *testing.T) {
	src := &FixtureSource{chunks: ChunkSamples(make([]float32, ChunkSize), ChunkSize)}
	ctx := context.Background()

	if _, err := src.ReadFrame(ctx); err != nil { // data frame
		t.Fatalf("ReadFrame() error = %v", err)
	}
	final, err := src.ReadFrame(ctx) // Final
	if err != nil || !final.Final {
		t.Fatalf("expected Final frame, got final=%v err=%v", final.Final, err)
	}

	src.Reset()
	f, err := src.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame() after Reset error = %v", err)
	}
	if f.Final || f.Empty() {
		t.Error("after Reset, expected a fresh data frame")
	}
	if f.Sequence != 1 {
		t.Errorf("after Reset Sequence = %d, want 1", f.Sequence)
	}
}
