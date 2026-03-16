package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	"github.com/gen2brain/malgo"
)

const (
	SampleRate = 16000
	Channels   = 1
	// ChunkSize is the number of samples per read (100ms at 16kHz).
	ChunkSize = 1600
)

// Capture handles microphone input using miniaudio.
type Capture struct {
	mu      sync.Mutex
	ctx     *malgo.AllocatedContext
	device  *malgo.Device
	buf     *RingBuffer
	running bool
}

// NewCapture creates a new mic capture instance.
func NewCapture() *Capture {
	return &Capture{
		buf: NewRingBuffer(SampleRate * 30), // 30 seconds buffer
	}
}

// Start begins capturing audio from the default input device.
func (c *Capture) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return nil
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("init audio context: %w", err)
	}
	c.ctx = mctx

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = Channels
	deviceConfig.SampleRate = SampleRate

	onData := func(outputSamples, inputSamples []byte, frameCount uint32) {
		samples := bytesToFloat32(inputSamples)
		c.buf.Write(samples)
	}

	device, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: onData,
	})
	if err != nil {
		mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("init capture device: %w", err)
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("start capture: %w", err)
	}

	c.device = device
	c.running = true

	// Stop when context is cancelled
	go func() {
		<-ctx.Done()
		c.Stop()
	}()

	return nil
}

// Stop stops audio capture and releases resources.
func (c *Capture) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return
	}

	c.device.Uninit()
	c.ctx.Uninit()
	c.ctx.Free()
	c.running = false
}

// Read returns the next chunk of audio samples.
// Returns nil if no data is available yet.
func (c *Capture) Read() []float32 {
	return c.buf.Read(ChunkSize)
}

// IsRunning returns whether capture is active.
func (c *Capture) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// bytesToFloat32 converts S16LE audio bytes to float32 samples in [-1.0, 1.0].
func bytesToFloat32(data []byte) []float32 {
	samples := make([]float32, len(data)/2)
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(data[i*2:]))
		samples[i] = float32(s) / float32(math.MaxInt16)
	}
	return samples
}
