package audio

import (
	"context"
	"time"
)

// FixtureSource replays WAV fixture audio in chunk-sized reads, optionally in real time.
type FixtureSource struct {
	chunks        [][]float32
	chunkDuration time.Duration
	realtime      bool
	index         int
	nextAt        time.Time
	seq           int64
	finalSent     bool
	closed        bool
}

// NewFixtureSourceFromWAV loads a WAV fixture and returns a chunked source.
func NewFixtureSourceFromWAV(path string, chunkSize int, realtime bool) (*FixtureSource, error) {
	samples, sampleRate, err := ReadWAVFloat32(path)
	if err != nil {
		return nil, err
	}
	if sampleRate != SampleRate {
		samples = resampleLinear(samples, sampleRate, SampleRate)
	}
	if chunkSize <= 0 {
		chunkSize = ChunkSize
	}

	return &FixtureSource{
		chunks:        ChunkSamples(samples, chunkSize),
		chunkDuration: SamplesDuration(chunkSize),
		realtime:      realtime,
		nextAt:        time.Now(),
	}, nil
}

// Read returns the next fixture chunk, or nil once exhausted.
func (f *FixtureSource) Read() []float32 {
	return f.nextChunk()
}

// nextChunk advances one chunk, honoring real-time pacing when enabled. It
// returns nil at end of input. Pacing only delays delivery; which chunks and
// when EOF occurs do not depend on the wall clock.
func (f *FixtureSource) nextChunk() []float32 {
	if f.index >= len(f.chunks) {
		return nil
	}
	if f.realtime {
		now := time.Now()
		if now.Before(f.nextAt) {
			time.Sleep(f.nextAt.Sub(now))
		}
	}

	chunk := append([]float32(nil), f.chunks[f.index]...)
	f.index++
	if f.realtime {
		f.nextAt = f.nextAt.Add(f.chunkDuration)
	}
	return chunk
}

// ReadFrame implements FrameSource for finite fixtures: each chunk is a
// SourceFixture frame, end of input is reported with one explicit Final frame,
// and any further read returns ErrSourceClosed. EOF is signalled explicitly, not
// inferred from silence, and frame content does not depend on the wall clock.
func (f *FixtureSource) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if f.closed {
		return Frame{}, ErrSourceClosed
	}

	chunk := f.nextChunk()
	if chunk == nil {
		if f.finalSent {
			return Frame{}, ErrSourceClosed
		}
		f.finalSent = true
		return Frame{SourceKind: SourceFixture, Final: true}, nil
	}

	f.seq++
	return Frame{
		Samples:    chunk,
		SampleRate: SampleRate,
		Channels:   Channels,
		Duration:   SamplesDuration(len(chunk)),
		Sequence:   f.seq,
		SourceKind: SourceFixture,
	}, nil
}

// Close implements FrameSource. Subsequent ReadFrame calls return ErrSourceClosed.
func (f *FixtureSource) Close() error {
	f.closed = true
	return nil
}

// Exhausted reports whether every fixture chunk has been read.
func (f *FixtureSource) Exhausted() bool {
	return f.index >= len(f.chunks)
}

// Reset replays the fixture from the beginning.
func (f *FixtureSource) Reset() {
	f.index = 0
	f.nextAt = time.Now()
	f.seq = 0
	f.finalSent = false
	f.closed = false
}

// ChunkSamples splits PCM into chunk-sized slices.
func ChunkSamples(samples []float32, chunkSize int) [][]float32 {
	if chunkSize <= 0 {
		chunkSize = ChunkSize
	}

	var chunks [][]float32
	for len(samples) > 0 {
		n := min(len(samples), chunkSize)
		chunk := append([]float32(nil), samples[:n]...)
		chunks = append(chunks, chunk)
		samples = samples[n:]
	}
	return chunks
}
