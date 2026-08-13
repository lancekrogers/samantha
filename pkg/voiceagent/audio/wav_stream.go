package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// wavHeaderSize is the fixed byte length of the canonical 44-byte mono PCM16
// WAV header written by WAVWriter (matching WriteWAVFloat32's layout).
const wavHeaderSize = 44

// WAVWriter streams mono 16-bit PCM to a WAV file incrementally. Each Write
// appends samples and patches the RIFF/data size fields in place, so a file
// left behind by a crash mid-recording is still a valid, readable WAV up to the
// last flushed chunk. This lets long recordings avoid buffering the whole
// capture in memory: callers stream chunks straight to disk and re-read the
// file when the complete PCM is finally needed (e.g. offline diarization).
//
// A WAVWriter is not safe for concurrent Writes; callers that fan in from
// multiple goroutines must serialize, exactly as the meeting capture loop does.
type WAVWriter struct {
	f       *os.File
	sampleN uint32
	closed  bool
}

// NewWAVWriter creates path (private, 0600) and writes a 44-byte header with
// zero-length data. Samples are appended by Write and the sizes are finalized
// on Close (and after every Write for crash resilience).
func NewWAVWriter(path string, sampleRate int) (*WAVWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("wav: invalid sample rate %d", sampleRate)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	w := &WAVWriter{f: f}
	if err := w.writeHeader(sampleRate); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return w, nil
}

func (w *WAVWriter) writeHeader(sampleRate int) error {
	var h [wavHeaderSize]byte
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36) // riff size for zero data
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(h[22:24], 1)  // mono
	binary.LittleEndian.PutUint32(h[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(h[32:34], 2)                    // block align
	binary.LittleEndian.PutUint16(h[34:36], 16)                   // bits/sample
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], 0) // data size for zero data
	_, err := w.f.Write(h[:])
	return err
}

// Write appends samples as little-endian int16 and patches the size fields so
// the file stays valid at every chunk boundary. Values are clamped to [-1, 1].
func (w *WAVWriter) Write(samples []float32) error {
	if w == nil || w.closed {
		return fmt.Errorf("wav: write to closed writer")
	}
	if len(samples) == 0 {
		return nil
	}
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := int16(clampFloat(float64(s), -1.0, 1.0) * float64(math.MaxInt16))
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	// Append at the current end-of-file offset; patchSizes uses WriteAt and
	// leaves this offset untouched so the next Write continues appending.
	if _, err := w.f.Write(buf); err != nil {
		return err
	}
	w.sampleN += uint32(len(samples))
	return w.patchSizes()
}

// patchSizes rewrites the RIFF and data length fields to match sampleN.
func (w *WAVWriter) patchSizes() error {
	dataSize := w.sampleN * 2
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], 36+dataSize)
	if _, err := w.f.WriteAt(b[:], 4); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(b[:], dataSize)
	if _, err := w.f.WriteAt(b[:], 40); err != nil {
		return err
	}
	return nil
}

// Samples reports how many samples have been written so far.
func (w *WAVWriter) Samples() int {
	if w == nil {
		return 0
	}
	return int(w.sampleN)
}

// Close finalizes the size fields and closes the file. Safe to call once; a
// second call is a no-op.
func (w *WAVWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	err := w.patchSizes()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}
