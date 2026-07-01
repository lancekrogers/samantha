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
	mu       sync.Mutex
	subsMu   sync.RWMutex
	ctx      *malgo.AllocatedContext
	device   *malgo.Device
	buf      *RingBuffer
	frontend Frontend
	running  bool
	subs     map[int]chan []float32
	nextSub  int
	frameSeq int64
}

// NewCapture creates a new mic capture instance.
func NewCapture() *Capture {
	return &Capture{
		buf:  NewRingBuffer(SampleRate * 30), // 30 seconds buffer
		subs: make(map[int]chan []float32),
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
		if c.frontend != nil {
			samples = c.frontend.ProcessCapture(samples)
		}
		c.buf.Write(samples)
		c.publish(samples)
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

// ReadFrame implements FrameSource for live capture: it returns the next chunk
// as a SourceLive frame, ErrNoFrameReady when no audio is buffered yet, and
// never sets Final. A live source ends only when ctx is canceled or Close is
// called, never on ordinary silence.
func (c *Capture) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	samples := c.buf.Read(ChunkSize)
	if samples == nil {
		return Frame{}, ErrNoFrameReady
	}
	c.frameSeq++
	return Frame{
		Samples:    samples,
		SampleRate: SampleRate,
		Channels:   Channels,
		Duration:   frameDuration(len(samples)),
		Sequence:   c.frameSeq,
		SourceKind: SourceLive,
	}, nil
}

// Close implements FrameSource by stopping capture and releasing the device.
func (c *Capture) Close() error {
	c.Stop()
	return nil
}

// Reset drains the ring buffer to discard stale audio between turns.
func (c *Capture) Reset() {
	for c.buf.Read(ChunkSize) != nil {
	}
}

// SetFrontend installs an audio front-end for live capture processing.
func (c *Capture) SetFrontend(frontend Frontend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frontend = frontend
}

// Subscribe registers a non-blocking listener for live capture chunks.
func (c *Capture) Subscribe(buffer int) (int, <-chan []float32) {
	if buffer <= 0 {
		buffer = 1
	}

	c.subsMu.Lock()
	defer c.subsMu.Unlock()

	id := c.nextSub
	c.nextSub++
	ch := make(chan []float32, buffer)
	c.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a capture listener.
func (c *Capture) Unsubscribe(id int) {
	c.subsMu.Lock()
	ch, ok := c.subs[id]
	if ok {
		delete(c.subs, id)
	}
	c.subsMu.Unlock()

	if ok {
		close(ch)
	}
}

// IsRunning returns whether capture is active.
func (c *Capture) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *Capture) publish(samples []float32) {
	c.subsMu.RLock()
	if len(c.subs) == 0 {
		c.subsMu.RUnlock()
		return
	}

	subs := make([]chan []float32, 0, len(c.subs))
	for _, ch := range c.subs {
		subs = append(subs, ch)
	}
	c.subsMu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- samples:
		default:
		}
	}
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
