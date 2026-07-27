package audio

// Frontend processes live microphone audio and can receive playback reference
// audio for echo cancellation or other front-end processing.
type Frontend interface {
	ProcessCapture(samples []float32) []float32
	PushPlaybackReference(samples []float32)
	Close() error
}

// ReferenceDelayer is optionally implemented by front-ends whose echo
// canceller needs to know how long after PushPlaybackReference the audio is
// actually audible. The player knows that number — it configured the device
// buffer — but most Frontend implementations (passthrough, test doubles) have
// no use for it, so it stays off the Frontend interface.
type ReferenceDelayer interface {
	SetReferenceDelay(samples int)
}

// PassthroughFrontend leaves audio unchanged while preserving the runtime hook
// points needed for a future AEC/NS/AGC implementation.
type PassthroughFrontend struct{}

// NewPassthroughFrontend creates a no-op audio front-end.
func NewPassthroughFrontend() *PassthroughFrontend {
	return &PassthroughFrontend{}
}

// ProcessCapture returns the input unchanged.
func (p *PassthroughFrontend) ProcessCapture(samples []float32) []float32 {
	return samples
}

// PushPlaybackReference records playback reference audio. The passthrough
// implementation intentionally ignores it.
func (p *PassthroughFrontend) PushPlaybackReference(samples []float32) {}

// Close releases front-end resources.
func (p *PassthroughFrontend) Close() error {
	return nil
}
