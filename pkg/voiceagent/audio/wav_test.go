package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWAVFloat32RejectsOversizedChunk(t *testing.T) {
	// WriteWAVFloat32 layout: fmt chunk size at byte 16, data chunk size at 40.
	tests := []struct {
		name       string
		sizeOffset int
	}{
		{"fmt chunk declares more bytes than file holds", 16},
		{"data chunk declares more bytes than file holds", 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "corrupt.wav")
			if err := WriteWAVFloat32(path, SampleRate, make([]float32, 64)); err != nil {
				t.Fatalf("WriteWAVFloat32() error = %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			binary.LittleEndian.PutUint32(data[tt.sizeOffset:], 0xFFFFFF00)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, _, err = ReadWAVFloat32(path)
			if err == nil {
				t.Fatal("ReadWAVFloat32() = nil error, want oversized-chunk rejection")
			}
			if !strings.Contains(err.Error(), "declares") {
				t.Fatalf("ReadWAVFloat32() error = %v, want oversized-chunk rejection", err)
			}
		})
	}
}

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
	if source.Exhausted() {
		t.Fatal("source.Exhausted() = true before reading")
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
	if !source.Exhausted() {
		t.Fatal("source.Exhausted() = false after reading all chunks")
	}

	source.Reset()
	if source.Exhausted() {
		t.Fatal("source.Exhausted() = true after reset")
	}
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
