package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// ReadWAVFloat32 reads a mono 16-bit PCM WAV file into float32 samples.
func ReadWAVFloat32(path string) ([]float32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat wav: %w", err)
	}
	fileSize := fi.Size()

	var header [12]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, 0, fmt.Errorf("read wav header: %w", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("unsupported wav header")
	}

	var sampleRate int
	var bitsPerSample uint16
	var channels uint16
	var data []byte

	for {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(f, chunkHeader[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, fmt.Errorf("read wav chunk header: %w", err)
		}

		chunkID := string(chunkHeader[0:4])
		chunkSize := binary.LittleEndian.Uint32(chunkHeader[4:8])

		// Reject sizes beyond the file's remaining bytes before allocating.
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, fmt.Errorf("seek wav: %w", err)
		}
		if remaining := fileSize - pos; int64(chunkSize) > remaining {
			return nil, 0, fmt.Errorf("wav chunk %s declares %d bytes but only %d remain", chunkID, chunkSize, remaining)
		}

		payload := make([]byte, chunkSize)
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil, 0, fmt.Errorf("read wav chunk %s: %w", chunkID, err)
		}

		switch chunkID {
		case "fmt ":
			if len(payload) < 16 {
				return nil, 0, fmt.Errorf("invalid fmt chunk")
			}
			audioFormat := binary.LittleEndian.Uint16(payload[0:2])
			channels = binary.LittleEndian.Uint16(payload[2:4])
			sampleRate = int(binary.LittleEndian.Uint32(payload[4:8]))
			bitsPerSample = binary.LittleEndian.Uint16(payload[14:16])
			if audioFormat != 1 {
				return nil, 0, fmt.Errorf("unsupported wav format %d", audioFormat)
			}
		case "data":
			data = payload
		}

		if chunkSize%2 == 1 {
			if _, err := f.Seek(1, io.SeekCurrent); err != nil {
				return nil, 0, fmt.Errorf("seek wav padding: %w", err)
			}
		}
	}

	if sampleRate == 0 || len(data) == 0 {
		return nil, 0, fmt.Errorf("missing wav fmt/data chunks")
	}
	if channels != 1 {
		return nil, 0, fmt.Errorf("unsupported wav channels %d", channels)
	}
	if bitsPerSample != 16 {
		return nil, 0, fmt.Errorf("unsupported wav bit depth %d", bitsPerSample)
	}

	samples := make([]float32, len(data)/2)
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(data[i*2:]))
		samples[i] = float32(s) / float32(math.MaxInt16)
	}
	return samples, sampleRate, nil
}

// WriteWAVFloat32 writes mono float32 PCM to a 16-bit PCM WAV file.
func WriteWAVFloat32(path string, sampleRate int, samples []float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dataSize := uint32(len(samples) * 2)
	riffSize := 36 + dataSize

	write := func(value any) error {
		return binary.Write(f, binary.LittleEndian, value)
	}

	if _, err := f.Write([]byte("RIFF")); err != nil {
		return err
	}
	if err := write(riffSize); err != nil {
		return err
	}
	if _, err := f.Write([]byte("WAVE")); err != nil {
		return err
	}
	if _, err := f.Write([]byte("fmt ")); err != nil {
		return err
	}
	if err := write(uint32(16)); err != nil {
		return err
	}
	if err := write(uint16(1)); err != nil {
		return err
	}
	if err := write(uint16(1)); err != nil {
		return err
	}
	if err := write(uint32(sampleRate)); err != nil {
		return err
	}
	if err := write(uint32(sampleRate * 2)); err != nil {
		return err
	}
	if err := write(uint16(2)); err != nil {
		return err
	}
	if err := write(uint16(16)); err != nil {
		return err
	}
	if _, err := f.Write([]byte("data")); err != nil {
		return err
	}
	if err := write(dataSize); err != nil {
		return err
	}

	for _, sample := range samples {
		clamped := clampFloat(float64(sample), -1.0, 1.0)
		value := int16(clamped * float64(math.MaxInt16))
		if err := write(value); err != nil {
			return err
		}
	}

	return nil
}
