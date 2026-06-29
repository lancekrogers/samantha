package audio

import (
	"time"
)

// FixtureSource replays WAV fixture audio in chunk-sized reads, optionally in real time.
type FixtureSource struct {
	chunks        [][]float32
	chunkDuration time.Duration
	realtime      bool
	index         int
	nextAt        time.Time
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
		chunkDuration: time.Duration(float64(chunkSize) / float64(SampleRate) * float64(time.Second)),
		realtime:      realtime,
		nextAt:        time.Now(),
	}, nil
}

// Read returns the next fixture chunk.
func (f *FixtureSource) Read() []float32 {
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

// Exhausted reports whether every fixture chunk has been read.
func (f *FixtureSource) Exhausted() bool {
	return f.index >= len(f.chunks)
}

// Reset replays the fixture from the beginning.
func (f *FixtureSource) Reset() {
	f.index = 0
	f.nextAt = time.Now()
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
