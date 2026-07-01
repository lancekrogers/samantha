package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	playbackChannels         = 1
	initialPlaybackBuffer    = 120 * time.Millisecond
	playbackCompactionFrames = 4096
)

// PlaybackResult reports the terminal state of a queued playback segment.
type PlaybackResult struct {
	Err         error
	Interrupted bool
}

// Playback exposes playback lifecycle signals for a single queued segment.
type Playback struct {
	started <-chan struct{}
	done    <-chan PlaybackResult
}

// NewPlayback constructs a playback handle from lifecycle channels.
func NewPlayback(started <-chan struct{}, done <-chan PlaybackResult) *Playback {
	return &Playback{started: started, done: done}
}

// Started closes when audible playback begins for the segment.
func (p *Playback) Started() <-chan struct{} {
	return p.started
}

// Done delivers the terminal playback result.
func (p *Playback) Done() <-chan PlaybackResult {
	return p.done
}

// Engine is the playback interface used by the pipeline.
type Engine interface {
	PlayStream(ctx context.Context, stream *PCMStream) (*Playback, error)
	Stop()
	IsPlaying() bool
	Close() error
}

// Player handles in-process audio playback through miniaudio.
type Player struct {
	mu         sync.Mutex
	ctx        *malgo.AllocatedContext
	device     *malgo.Device
	sampleRate int
	frontend   Frontend
	current    *playbackSegment
	queue      []*playbackSegment
	playing    atomic.Bool
	closed     bool
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return &Player{}
}

// SetFrontend installs an audio front-end that can observe playback
// reference audio for echo cancellation or similar processing.
func (p *Player) SetFrontend(frontend Frontend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frontend = frontend
}

// PlayStream queues a synthesized PCM stream for playback.
func (p *Player) PlayStream(ctx context.Context, stream *PCMStream) (*Playback, error) {
	if stream == nil {
		return nil, errors.New("nil pcm stream")
	}

	segment := newPlaybackSegment()
	go p.pumpSegment(ctx, segment, stream)

	if err := segment.waitReady(ctx); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, errors.New("audio player is closed")
	}

	p.queue = append(p.queue, segment)
	return segment.playback(), nil
}

// Stop interrupts current playback and clears any queued audio.
func (p *Player) Stop() {
	p.mu.Lock()
	segments := make([]*playbackSegment, 0, len(p.queue)+1)
	if p.current != nil {
		segments = append(segments, p.current)
		p.current = nil
	}
	segments = append(segments, p.queue...)
	p.queue = nil
	p.mu.Unlock()

	p.playing.Store(false)
	for _, segment := range segments {
		segment.interrupt()
	}
}

// IsPlaying returns whether audio is currently audible.
func (p *Player) IsPlaying() bool {
	return p.playing.Load()
}

// Close releases audio resources.
func (p *Player) Close() error {
	p.Stop()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	if p.device != nil {
		_ = p.device.Stop()
		p.device.Uninit()
		p.device = nil
	}

	if p.ctx != nil {
		_ = p.ctx.Uninit()
		p.ctx.Free()
		p.ctx = nil
	}

	return nil
}

func (p *Player) pumpSegment(ctx context.Context, segment *playbackSegment, stream *PCMStream) {
	inputRate, err := stream.WaitReady(ctx)
	if err != nil {
		segment.fail(err)
		return
	}

	outputRate, err := p.ensureDevice(inputRate)
	if err != nil {
		segment.fail(err)
		return
	}

	segment.setReadyFrames(int(float64(outputRate) * initialPlaybackBuffer.Seconds()))

	for {
		select {
		case <-ctx.Done():
			segment.fail(ctx.Err())
			return
		case frames, ok := <-stream.Frames():
			if !ok {
				segment.finishInput(stream.Err())
				return
			}
			if len(frames) == 0 {
				continue
			}

			if inputRate != outputRate {
				frames = resampleLinear(frames, inputRate, outputRate)
			}

			p.mu.Lock()
			frontend := p.frontend
			p.mu.Unlock()
			if frontend != nil {
				frontend.PushPlaybackReference(frames)
			}

			segment.append(float32ToPCM16(frames))
		}
	}
}

func (p *Player) ensureDevice(sampleRate int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return 0, errors.New("audio player is closed")
	}

	if p.device != nil {
		return p.sampleRate, nil
	}

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return 0, err
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = playbackChannels
	deviceConfig.SampleRate = uint32(sampleRate)
	deviceConfig.Alsa.NoMMap = 1

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: p.onData,
	})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}

	p.ctx = ctx
	p.device = device
	p.sampleRate = sampleRate
	return p.sampleRate, nil
}

func (p *Player) onData(outputSamples, inputSamples []byte, frameCount uint32) {
	clearBytes(outputSamples)

	framesRemaining := int(frameCount)
	offsetBytes := 0

	for framesRemaining > 0 {
		segment := p.currentSegment()
		if segment == nil {
			p.playing.Store(false)
			return
		}

		written, finished := segment.writeTo(outputSamples[offsetBytes:], framesRemaining)
		if written == 0 {
			if finished {
				p.finishSegment(segment)
				continue
			}

			p.playing.Store(false)
			return
		}

		p.playing.Store(true)
		offsetBytes += written * 2
		framesRemaining -= written

		if finished {
			p.finishSegment(segment)
		}
	}
}

func (p *Player) currentSegment() *playbackSegment {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.current == nil && len(p.queue) > 0 {
		p.current = p.queue[0]
		p.queue = p.queue[1:]
	}

	return p.current
}

func (p *Player) finishSegment(segment *playbackSegment) {
	p.mu.Lock()
	if p.current == segment {
		p.current = nil
	}
	hasMore := p.current != nil || len(p.queue) > 0
	p.mu.Unlock()

	if !hasMore {
		p.playing.Store(false)
	}

	segment.complete()
}

type playbackSegment struct {
	mu          sync.Mutex
	samples     []int16
	offset      int
	readyFrames int
	inputDone   bool
	err         error
	started     sync.Once
	done        sync.Once
	ready       chan struct{}
	startedCh   chan struct{}
	doneCh      chan PlaybackResult
	readyClosed bool
	interrupted bool
}

func newPlaybackSegment() *playbackSegment {
	return &playbackSegment{
		ready:     make(chan struct{}),
		startedCh: make(chan struct{}),
		doneCh:    make(chan PlaybackResult, 1),
	}
}

func (s *playbackSegment) playback() *Playback {
	return &Playback{
		started: s.startedCh,
		done:    s.doneCh,
	}
}

func (s *playbackSegment) setReadyFrames(frames int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if frames <= 0 {
		frames = 1
	}
	s.readyFrames = frames
	s.maybeReadyLocked()
}

func (s *playbackSegment) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ready:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingLocked() > 0 {
		return nil
	}
	if s.interrupted {
		return context.Canceled
	}
	if s.err != nil {
		return s.err
	}
	return errors.New("pcm stream produced no samples")
}

func (s *playbackSegment) append(samples []int16) {
	if len(samples) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inputDone || s.interrupted {
		return
	}

	if s.offset > 0 && s.offset == len(s.samples) {
		s.samples = s.samples[:0]
		s.offset = 0
	}

	s.samples = append(s.samples, samples...)
	s.maybeReadyLocked()
}

func (s *playbackSegment) finishInput(err error) {
	s.mu.Lock()
	if s.inputDone {
		s.mu.Unlock()
		return
	}
	s.inputDone = true
	s.err = err
	s.maybeReadyLocked()
	shouldComplete := s.pendingLocked() == 0
	interrupted := s.interrupted
	resultErr := s.err
	s.mu.Unlock()

	if shouldComplete {
		s.finish(PlaybackResult{Err: resultErr, Interrupted: interrupted})
	}
}

func (s *playbackSegment) fail(err error) {
	s.mu.Lock()
	s.inputDone = true
	s.err = err
	s.maybeReadyLocked()
	s.mu.Unlock()

	s.finish(PlaybackResult{Err: err})
}

func (s *playbackSegment) interrupt() {
	s.mu.Lock()
	s.inputDone = true
	s.interrupted = true
	s.samples = nil
	s.offset = 0
	s.maybeReadyLocked()
	s.mu.Unlock()

	s.finish(PlaybackResult{Interrupted: true})
}

func (s *playbackSegment) complete() {
	s.mu.Lock()
	result := PlaybackResult{
		Err:         s.err,
		Interrupted: s.interrupted,
	}
	s.mu.Unlock()

	s.finish(result)
}

func (s *playbackSegment) writeTo(output []byte, maxFrames int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.pendingLocked()
	if available == 0 {
		return 0, s.inputDone
	}

	if maxFrames > available {
		maxFrames = available
	}

	s.started.Do(func() { close(s.startedCh) })

	for i := range maxFrames {
		binary.LittleEndian.PutUint16(output[i*2:], uint16(s.samples[s.offset+i]))
	}
	s.offset += maxFrames

	if s.offset >= len(s.samples) {
		s.samples = s.samples[:0]
		s.offset = 0
	} else if s.offset >= playbackCompactionFrames && s.offset*2 >= len(s.samples) {
		copy(s.samples, s.samples[s.offset:])
		s.samples = s.samples[:len(s.samples)-s.offset]
		s.offset = 0
	}

	return maxFrames, s.pendingLocked() == 0 && s.inputDone
}

func (s *playbackSegment) pendingLocked() int {
	return len(s.samples) - s.offset
}

func (s *playbackSegment) maybeReadyLocked() {
	if s.readyClosed {
		return
	}

	pending := s.pendingLocked()
	switch {
	case pending > 0 && (s.readyFrames == 0 || pending >= s.readyFrames):
		s.readyClosed = true
		close(s.ready)
	case s.inputDone:
		s.readyClosed = true
		close(s.ready)
	}
}

func (s *playbackSegment) finish(result PlaybackResult) {
	s.done.Do(func() {
		s.doneCh <- result
		close(s.doneCh)
	})
}

func clearBytes(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
}

func float32ToPCM16(samples []float32) []int16 {
	pcm := make([]int16, len(samples))
	for i, sample := range samples {
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}
		pcm[i] = int16(sample * float32(math.MaxInt16))
	}
	return pcm
}

func resampleLinear(samples []float32, from, to int) []float32 {
	if len(samples) == 0 || from <= 0 || to <= 0 || from == to {
		return samples
	}

	outLen := int(math.Round(float64(len(samples)) * float64(to) / float64(from)))
	if outLen < 1 {
		outLen = 1
	}

	out := make([]float32, outLen)
	step := float64(from) / float64(to)
	for i := range outLen {
		pos := float64(i) * step
		index := int(pos)
		if index >= len(samples)-1 {
			out[i] = samples[len(samples)-1]
			continue
		}

		frac := float32(pos - float64(index))
		a := samples[index]
		b := samples[index+1]
		out[i] = a + (b-a)*frac
	}

	return out
}
