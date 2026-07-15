package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
)

const (
	playbackChannels         = 1
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
// Engine plays synthesized PCM. PlayStream takes ownership of stream even when
// it returns an error: implementations must drain (or cancel) the stream so
// synth producers are not left blocked on a full frames channel.
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
	channels   int // device client channel count (mono TTS is expanded into this)
	frontend   Frontend
	current    *playbackSegment
	queue      []*playbackSegment
	playing    atomic.Bool
	closed     bool
	deviceName string
	debugRoot  string
	debug      *playerDebugRecorder
	// monoScratch holds one callback period of mono S16 while onData expands
	// into the multi-channel device buffer. Sized lazily under the callback.
	monoScratch []byte
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return NewPlayerWithDevice("")
}

// NewPlayerWithDevice creates a player routed to deviceName. An empty name
// follows the operating-system default.
func NewPlayerWithDevice(deviceName string) *Player {
	return &Player{deviceName: deviceName, debugRoot: debugAudioDir()}
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
	if p.debug != nil {
		p.debug.close()
		p.debug = nil
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

	// Kokoro produces the complete sentence before exposing its PCM samples.
	// Buffer that already-generated sentence before handing it to the real-time
	// device callback. Starting from a partial buffer lets a brief scheduler
	// delay drain the segment and insert silence in the middle of speech.
	segment.setReadyFrames(0)
	p.mu.Lock()
	debug := p.debug
	p.mu.Unlock()
	var samples []float32

	for {
		select {
		case <-ctx.Done():
			segment.fail(ctx.Err())
			return
		case frames, ok := <-stream.Frames():
			if !ok {
				p.mu.Lock()
				frontend := p.frontend
				p.mu.Unlock()
				finalizeSegment(segment, frontend, debug, samples, inputRate, outputRate, stream.Err())
				return
			}
			if len(frames) == 0 {
				continue
			}
			samples = append(samples, frames...)
		}
	}
}

// finalizeSegment resamples the fully-buffered utterance once and appends it
// to segment regardless of streamErr: a stream that fails partway through
// (e.g. a cancelled turn) may already have produced audio worth playing, and
// the caller still learns about the failure through the segment's terminal
// PlaybackResult once that audio finishes.
func finalizeSegment(segment *playbackSegment, frontend Frontend, debug *playerDebugRecorder, samples []float32, inputRate, outputRate int, streamErr error) {
	if debug != nil {
		debug.captureSource(inputRate, samples)
	}
	if inputRate != outputRate {
		samples = resample(samples, inputRate, outputRate)
	}
	if frontend != nil {
		frontend.PushPlaybackReference(samples)
	}
	segment.append(float32ToPCM16(samples))
	segment.finishInput(streamErr)
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
	deviceConfig.Alsa.NoMMap = 1
	if err := selectDevice(ctx.Context, malgo.Playback, p.deviceName, &deviceConfig.Playback); err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, fmt.Errorf("select playback device: %w", err)
	}
	actualDeviceName, nativeRate, nativeChannels, err := playbackDeviceDetails(ctx.Context, p.deviceName, sampleRate)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}
	if nativeRate == 0 {
		nativeRate = uint32(sampleRate)
	}
	// Never hard-code mono here: multi-channel devices (Studio Display Speakers
	// advertise 8ch) crackled when CoreAudio invented a mono upmix. Layout is
	// pinned by TestStudioDisplayClientLayoutIsStereo and expand tests.
	channels := choosePlaybackChannels(nativeChannels)
	deviceConfig.Playback.Channels = channels
	deviceConfig.SampleRate = nativeRate

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: p.onData,
	})
	if err != nil {
		// Stereo is preferred. If the device rejects it, try the advertised
		// native multi-channel layout (still expanded from mono in onData).
		// Never silently fall back to mono on multi-channel hardware — that
		// path is the Studio Display crackle regression.
		if channels != 1 && nativeChannels > 2 {
			deviceConfig.Playback.Channels = nativeChannels
			device, err = malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
				Data: p.onData,
			})
		}
		if err != nil && channels != 1 && nativeChannels <= 1 {
			// Truly mono-only device: last resort.
			deviceConfig.Playback.Channels = 1
			device, err = malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
				Data: p.onData,
			})
		}
		if err != nil {
			_ = ctx.Uninit()
			ctx.Free()
			return 0, err
		}
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}
	deviceRate := int(device.SampleRate())
	if deviceRate <= 0 {
		deviceRate = sampleRate
	}
	deviceChannels := int(device.PlaybackChannels())
	if deviceChannels <= 0 {
		deviceChannels = int(channels)
	}
	if p.debugRoot != "" {
		debug, err := newPlayerDebugRecorder(p.debugRoot, actualDeviceName, sampleRate, deviceRate, deviceChannels)
		if err != nil {
			_ = device.Stop()
			device.Uninit()
			_ = ctx.Uninit()
			ctx.Free()
			return 0, fmt.Errorf("start audio debug capture: %w", err)
		}
		p.debug = debug
	}

	p.ctx = ctx
	p.device = device
	p.sampleRate = deviceRate
	p.channels = deviceChannels
	return p.sampleRate, nil
}

func (p *Player) onData(outputSamples, inputSamples []byte, frameCount uint32) {
	clearBytes(outputSamples)

	p.mu.Lock()
	channels := p.channels
	if channels <= 0 {
		channels = playbackChannels
	}
	monoNeed := int(frameCount) * 2
	if cap(p.monoScratch) < monoNeed {
		p.monoScratch = make([]byte, monoNeed)
	} else {
		p.monoScratch = p.monoScratch[:monoNeed]
	}
	monoBuf := p.monoScratch
	debug := p.debug
	p.mu.Unlock()
	clearBytes(monoBuf)

	framesRemaining := int(frameCount)
	monoOffset := 0
	writtenFrames := 0
	active := false
	defer func() {
		if !active {
			return
		}
		// Expand mono → device layout, then capture the exact multi-channel
		// buffer handed to miniaudio.
		expandMonoS16LE(monoBuf[:writtenFrames*2], writtenFrames, channels, outputSamples)
		if debug != nil {
			debug.captureCallback(outputSamples, int(frameCount), writtenFrames)
		}
	}()

	for framesRemaining > 0 {
		segment := p.currentSegment()
		if segment == nil {
			p.playing.Store(false)
			return
		}
		active = true

		written, finished := segment.writeTo(monoBuf[monoOffset:], framesRemaining)
		if written == 0 {
			if finished {
				p.finishSegment(segment)
				continue
			}

			p.playing.Store(false)
			return
		}

		p.playing.Store(true)
		writtenFrames += written
		monoOffset += written * 2
		framesRemaining -= written

		if finished {
			p.finishSegment(segment)
		}
	}
}

// expandMonoS16LE writes mono S16LE frames into an interleaved multi-channel
// S16LE buffer. Channel 0 (and channel 1 when present) carry the mono signal;
// additional channels are left silent so multi-channel devices do not play
// garbage on rear/height buses.
func expandMonoS16LE(mono []byte, frames, channels int, out []byte) {
	if frames <= 0 || channels <= 0 {
		return
	}
	if channels == 1 {
		copy(out, mono[:frames*2])
		return
	}
	for i := 0; i < frames; i++ {
		s0 := mono[i*2]
		s1 := mono[i*2+1]
		base := i * channels * 2
		// Front L
		out[base] = s0
		out[base+1] = s1
		if channels > 1 {
			// Front R (duplicate mono)
			out[base+2] = s0
			out[base+3] = s1
		}
		// Remaining channels stay 0 from clearBytes.
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
	case pending > 0 && s.readyFrames > 0 && pending >= s.readyFrames:
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

// resample converts mono PCM between sample rates for playback. Prefer calling
// this on a complete utterance (see finalizeSegment). Integer-ratio upsamples
// (e.g. 24 kHz → 48 kHz) use an exact polyphase path; other ratios use
// Catmull-Rom cubic interpolation, which is far less harsh on Kokoro's HF
// content than linear interpolation at 24→44.1.
func resample(samples []float32, from, to int) []float32 {
	if len(samples) == 0 || from <= 0 || to <= 0 || from == to {
		return samples
	}
	if to%from == 0 {
		return upsampleInteger(samples, to/from)
	}
	return resampleCubic(samples, from, to)
}

// upsampleInteger produces exactly len(samples)*factor frames for exact integer
// upsampling (factor=2 → 24 kHz to 48 kHz). Between input samples it uses
// Catmull-Rom; after the final input sample it holds that value for the
// remaining sub-frames so duration stays n/from seconds.
func upsampleInteger(samples []float32, factor int) []float32 {
	if factor <= 1 || len(samples) == 0 {
		return samples
	}
	out := make([]float32, len(samples)*factor)
	for i := 0; i < len(samples); i++ {
		out[i*factor] = samples[i]
		for f := 1; f < factor; f++ {
			if i < len(samples)-1 {
				t := float64(f) / float64(factor)
				out[i*factor+f] = sampleCubic(samples, float64(i)+t)
			} else {
				out[i*factor+f] = samples[i]
			}
		}
	}
	return out
}

func resampleCubic(samples []float32, from, to int) []float32 {
	outLen := int(math.Round(float64(len(samples)) * float64(to) / float64(from)))
	if outLen < 1 {
		outLen = 1
	}
	out := make([]float32, outLen)
	step := float64(from) / float64(to)
	for i := range outLen {
		out[i] = sampleCubic(samples, float64(i)*step)
	}
	return out
}

// sampleCubic evaluates a Catmull-Rom spline at a fractional sample index.
func sampleCubic(samples []float32, pos float64) float32 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	if pos <= 0 {
		return samples[0]
	}
	if pos >= float64(n-1) {
		return samples[n-1]
	}
	i := int(pos)
	t := float32(pos - float64(i))
	y0 := samples[max(i-1, 0)]
	y1 := samples[i]
	y2 := samples[min(i+1, n-1)]
	y3 := samples[min(i+2, n-1)]
	// Catmull-Rom: 0.5 * (2*y1 + (-y0+y2)*t + (2*y0-5*y1+4*y2-y3)*t^2 + (-y0+3*y1-3*y2+y3)*t^3)
	t2 := t * t
	t3 := t2 * t
	return 0.5 * ((2 * y1) +
		(-y0+y2)*t +
		(2*y0-5*y1+4*y2-y3)*t2 +
		(-y0+3*y1-3*y2+y3)*t3)
}

// resampleLinear remains for tests and call sites that explicitly want the
// historical linear kernel (e.g. characterizing boundary discontinuities).
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
