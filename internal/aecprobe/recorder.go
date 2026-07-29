package aecprobe

import (
	"sync"

	"github.com/lancekrogers/samantha/internal/audio"
)

// Recorder wraps a real audio.Frontend and records every stream that crosses
// it, without changing what the front-end does. The probe installs it on both
// the player and the capture device, so a run observes exactly the production
// path — same resampler, same reference delay, same canceller — and nothing in
// internal/audio has to grow a test hook.
//
// Three streams matter, and scoring needs all three:
//
//	reference — what PushPlaybackReference actually received, after the
//	            ingress worker resampled to the capture clock. This is the
//	            signal whose alignment is the whole question.
//	micIn     — the raw microphone, echo included. The denominator of ERLE.
//	micOut    — what ProcessCapture returned, which is what VAD sees and
//	            therefore what decides whether barge-in trips.
type Recorder struct {
	inner audio.Frontend

	mu        sync.Mutex
	reference []float32
	micIn     []float32
	micOut    []float32
	// delay is the offset the player published, recorded so metrics.json can
	// report the estimate alongside the delay actually measured. The gap
	// between them is the number that says whether referenceDelaySamples is
	// guessing well.
	delay     int
	delaySeen bool
	refPushes int
	capChunks int
	// refAnchor is how many microphone samples had been recorded when the very
	// first reference block arrived. It converts the reference stream — which
	// only advances while audio plays — into microphone-stream coordinates, so
	// the two can be correlated against each other.
	//
	// Without it the only available measurement is "stimulus generated" to
	// "echo heard", which also counts device initialisation and stream setup.
	// That inflated the first hardware runs to ~153-165ms with 12ms of
	// run-to-run scatter that was pure scheduling jitter.
	refAnchor    int
	refAnchorSet bool
}

// NewRecorder wraps inner. inner is used, not replaced: the probe measures the
// real front-end, so a run reflects the shipping configuration.
func NewRecorder(inner audio.Frontend) *Recorder {
	return &Recorder{inner: inner}
}

// ProcessCapture records the microphone on both sides of the front-end.
func (r *Recorder) ProcessCapture(samples []float32) []float32 {
	// Copy before handing over: ProcessCapture is free to return the same
	// backing array it was given, and Capture fans one slice to every
	// subscriber, so retaining either without a copy would alias live audio.
	in := append([]float32(nil), samples...)
	out := r.inner.ProcessCapture(samples)

	r.mu.Lock()
	r.micIn = append(r.micIn, in...)
	r.micOut = append(r.micOut, out...)
	r.capChunks++
	r.mu.Unlock()
	return out
}

// PushPlaybackReference records the far-end reference as the AEC receives it.
func (r *Recorder) PushPlaybackReference(samples []float32) {
	r.mu.Lock()
	if !r.refAnchorSet {
		r.refAnchor = len(r.micIn)
		r.refAnchorSet = true
	}
	r.reference = append(r.reference, samples...)
	r.refPushes++
	r.mu.Unlock()
	r.inner.PushPlaybackReference(samples)
}

// SetReferenceDelay forwards the player's offset and records it. Implementing
// this is what keeps the decorator transparent: Player type-asserts
// audio.ReferenceDelayer, so a wrapper that did not forward it would silently
// disable the delay compensation and the probe would measure the old bug.
func (r *Recorder) SetReferenceDelay(samples int) {
	r.mu.Lock()
	r.delay = samples
	r.delaySeen = true
	r.mu.Unlock()

	if d, ok := r.inner.(audio.ReferenceDelayer); ok {
		d.SetReferenceDelay(samples)
	}
}

// Close releases the wrapped front-end.
func (r *Recorder) Close() error { return r.inner.Close() }

// Streams returns copies of everything recorded so far.
func (r *Recorder) Streams() (reference, micIn, micOut []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]float32(nil), r.reference...),
		append([]float32(nil), r.micIn...),
		append([]float32(nil), r.micOut...)
}

// PublishedDelay reports the offset the player published, and whether it ever
// did. False means the player never reached applyReferenceDelay with a device
// rate — the run measured an uncompensated front-end and its numbers describe
// the old behaviour, not the current one.
func (r *Recorder) PublishedDelay() (samples int, published bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delay, r.delaySeen
}

// Counts reports how many reference pushes and capture chunks were seen, so a
// run that recorded nothing is distinguishable from one that cancelled well.
func (r *Recorder) Counts() (refPushes, captureChunks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refPushes, r.capChunks
}

// ReferenceAnchor reports the microphone-stream offset at which the first
// reference block was pushed, and whether any was. Correlating the reference
// against micIn from this offset measures the lag the canceller actually has to
// span: reference-pushed to echo-heard, with no device-setup time in it.
func (r *Recorder) ReferenceAnchor() (offset int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refAnchor, r.refAnchorSet
}

// Mark returns the current length of the microphone recording. The probe takes
// one before and after each phase so scoring can slice per phase without
// needing wall-clock timestamps to line up across three streams.
func (r *Recorder) Mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.micIn)
}
