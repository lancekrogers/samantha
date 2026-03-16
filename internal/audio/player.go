package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Player handles audio playback.
type Player struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	playing bool
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return &Player{}
}

// PlayWAV plays raw float32 audio samples by writing a temp WAV and
// using the system audio player.
func (p *Player) PlayWAV(ctx context.Context, samples []float32, sampleRate int) error {
	p.mu.Lock()
	p.playing = true
	ctx, p.cancel = context.WithCancel(ctx)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.playing = false
		p.cancel = nil
		p.mu.Unlock()
	}()

	tmpDir := filepath.Join(os.TempDir(), "samantha_audio")
	_ = os.MkdirAll(tmpDir, 0o755)
	wavPath := filepath.Join(tmpDir, "playback.wav")

	if err := WriteWAV(wavPath, samples, sampleRate); err != nil {
		return fmt.Errorf("write WAV: %w", err)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(ctx, "afplay", wavPath)
	} else {
		cmd = exec.CommandContext(ctx, "aplay", wavPath)
	}

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil // cancelled
		}
		return fmt.Errorf("playback: %w", err)
	}
	return nil
}

// PlayAsync plays audio in a background goroutine.
func (p *Player) PlayAsync(ctx context.Context, samples []float32, sampleRate int) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.PlayWAV(ctx, samples, sampleRate)
	}()
	return done
}

// Stop cancels any active playback.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
}

// IsPlaying returns whether audio is currently playing.
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing
}

// WriteWAV writes float32 samples as a 16-bit PCM WAV file.
func WriteWAV(path string, samples []float32, sampleRate int) error {
	pcm := make([]byte, len(samples)*2)
	for i, s := range samples {
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		v := int16(s * float32(math.MaxInt16))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	channels := 1
	bitsPerSample := 16
	dataSize := len(pcm)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	// RIFF header
	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, uint32(36+dataSize))
	f.Write([]byte("WAVE"))

	// fmt chunk
	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, uint32(16))
	binary.Write(f, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(f, binary.LittleEndian, uint16(channels))
	binary.Write(f, binary.LittleEndian, uint32(sampleRate))
	binary.Write(f, binary.LittleEndian, uint32(byteRate))
	binary.Write(f, binary.LittleEndian, uint16(blockAlign))
	binary.Write(f, binary.LittleEndian, uint16(bitsPerSample))

	// data chunk
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, uint32(dataSize))
	_, err = f.Write(pcm)
	return err
}
