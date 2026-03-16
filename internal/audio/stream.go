package audio

import (
	"context"
	"errors"
	"sync"
)

// PCMStream carries synthesized mono PCM frames for playback.
type PCMStream struct {
	mu         sync.Mutex
	sampleRate int
	err        error
	closed     bool
	ready      chan struct{}
	frames     chan []float32
}

// NewPCMStream creates a new PCM stream.
func NewPCMStream() *PCMStream {
	return &PCMStream{
		ready:  make(chan struct{}),
		frames: make(chan []float32, 8),
	}
}

// SetSampleRate sets the stream sample rate once before frames are written.
func (s *PCMStream) SetSampleRate(sampleRate int) error {
	if sampleRate <= 0 {
		return errors.New("sample rate must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sampleRate != 0 {
		if s.sampleRate != sampleRate {
			return errors.New("sample rate already set")
		}
		return nil
	}

	s.sampleRate = sampleRate
	close(s.ready)
	return nil
}

// WaitReady waits until the stream sample rate is known or the stream fails.
func (s *PCMStream) WaitReady(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.ready:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sampleRate == 0 {
			if s.err != nil {
				return 0, s.err
			}
			return 0, errors.New("pcm stream closed before sample rate was set")
		}
		return s.sampleRate, nil
	}
}

// Frames returns the stream's PCM frame channel.
func (s *PCMStream) Frames() <-chan []float32 {
	return s.frames
}

// Write pushes a frame batch into the stream.
func (s *PCMStream) Write(samples []float32) error {
	s.mu.Lock()
	if s.closed {
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return errors.New("pcm stream is closed")
	}
	if s.sampleRate == 0 {
		s.mu.Unlock()
		return errors.New("sample rate must be set before writing frames")
	}
	s.mu.Unlock()

	if len(samples) == 0 {
		return nil
	}

	chunk := make([]float32, len(samples))
	copy(chunk, samples)

	s.frames <- chunk
	return nil
}

// Close marks the stream complete.
func (s *PCMStream) Close() {
	s.CloseWithError(nil)
}

// CloseWithError marks the stream complete with an optional error.
func (s *PCMStream) CloseWithError(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.err = err
	if s.sampleRate == 0 {
		close(s.ready)
	}
	close(s.frames)
	s.mu.Unlock()
}

// Err returns the terminal stream error, if any.
func (s *PCMStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
