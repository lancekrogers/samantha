package audio

import (
	"path/filepath"
	"testing"
)

func TestWriteReadWAVFloat32RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.wav")
	input := make([]float32, 1024)
	for i := range input {
		input[i] = voicedSample(i, 0.25)
	}

	if err := WriteWAVFloat32(path, SampleRate, input); err != nil {
		t.Fatalf("WriteWAVFloat32() error = %v", err)
	}

	output, sampleRate, err := ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32() error = %v", err)
	}
	if sampleRate != SampleRate {
		t.Fatalf("sampleRate = %d, want %d", sampleRate, SampleRate)
	}
	if len(output) != len(input) {
		t.Fatalf("len(output) = %d, want %d", len(output), len(input))
	}
	if diff := meanAbsDiff(input, output); diff > 0.002 {
		t.Fatalf("meanAbsDiff = %.4f, want <= 0.002", diff)
	}
}

func TestFixtureSourceReadsFixtureSequentially(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.wav")
	input := make([]float32, ChunkSize*3+200)
	for i := range input {
		input[i] = voicedSample(i, 0.2)
	}
	if err := WriteWAVFloat32(path, SampleRate, input); err != nil {
		t.Fatalf("WriteWAVFloat32() error = %v", err)
	}

	source, err := NewFixtureSourceFromWAV(path, ChunkSize, false)
	if err != nil {
		t.Fatalf("NewFixtureSourceFromWAV() error = %v", err)
	}

	var total int
	for {
		chunk := source.Read()
		if len(chunk) == 0 {
			break
		}
		total += len(chunk)
	}

	if total != len(input) {
		t.Fatalf("total samples = %d, want %d", total, len(input))
	}

	source.Reset()
	if first := source.Read(); len(first) != ChunkSize {
		t.Fatalf("len(first chunk after reset) = %d, want %d", len(first), ChunkSize)
	}
}

func meanAbsDiff(a, b []float32) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}

	sum := 0.0
	for i := range n {
		diff := a[i] - b[i]
		if diff < 0 {
			sum -= float64(diff)
		} else {
			sum += float64(diff)
		}
	}
	return sum / float64(n)
}
