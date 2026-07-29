package audio

import "sync"

// delayCalibrator collects one contiguous window of far-end reference and the
// microphone audio recorded alongside it, so the true reference-to-echo lag can
// be correlated off the audio callbacks.
//
// Both callbacks only append here; correlation is O(maxLag*window) and runs on
// whoever calls Ready/Measure, never on the device threads.
//
// The alignment trick is the same one the hardware probe uses: the reference
// stream only advances while audio plays, so it cannot be indexed against the
// microphone directly. Recording how many microphone samples had been seen when
// the window opened places the two in the same coordinates.
type delayCalibrator struct {
	mu sync.Mutex

	// ref is the contiguous reference collected since the window opened.
	ref []float32
	// mic holds microphone audio from the window's anchor onward. Correlation
	// needs the reference window plus the whole lag search beyond it.
	mic []float32
	// collecting is false between windows, when the calibrator is either idle
	// or holding a complete window waiting to be measured.
	collecting bool
	// complete marks a window with enough of both streams to correlate.
	complete bool
	// sinceRef counts microphone samples observed since the last reference
	// push, which is how a pause in playback is detected without a clock.
	sinceRef int
	// applied is the delay most recently adopted, so a re-measurement that
	// agrees does not churn the queue.
	applied int
}

// observeReference records far-end audio. Called from PushPlaybackReference.
func (c *delayCalibrator) observeReference(samples []float32) {
	if len(samples) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.complete {
		return // holding a window for measurement
	}
	// A gap in playback breaks contiguity, so start over rather than
	// correlating a reference that has silence spliced into it.
	if c.collecting && c.sinceRef > calibrationGapSamples {
		c.reset()
	}
	c.collecting = true
	c.sinceRef = 0

	if len(c.ref) < calibrationWindow {
		c.ref = append(c.ref, samples...)
	}
}

// observeMic records raw microphone audio. Called from ProcessCapture with the
// pre-processing signal: correlating against the canceller's output would be
// circular, since a working canceller removes the very echo being looked for.
func (c *delayCalibrator) observeMic(samples []float32) {
	if len(samples) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.collecting || c.complete {
		return
	}
	c.sinceRef += len(samples)

	need := calibrationWindow + calibrationMaxLag
	if len(c.mic) < need {
		c.mic = append(c.mic, samples...)
	}
	if len(c.ref) >= calibrationWindow && len(c.mic) >= need {
		c.complete = true
	}
}

// takeWindow hands out a completed window and clears it, so the next collection
// starts fresh. Returns ok=false when no window is ready.
func (c *delayCalibrator) takeWindow() (ref, mic []float32, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.complete {
		return nil, nil, false
	}
	ref, mic = c.ref, c.mic
	c.ref, c.mic = nil, nil
	c.complete = false
	c.collecting = false
	c.sinceRef = 0
	return ref, mic, true
}

// noteApplied records an adopted delay so a later identical measurement can be
// skipped rather than resetting the reference queue for no change.
func (c *delayCalibrator) noteApplied(delay int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applied = delay
}

// appliedDelay reports the last adopted measurement, zero if none.
func (c *delayCalibrator) appliedDelay() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applied
}

// forget discards any collected window and the adopted measurement, returning
// the calibrator to the state it had before it ever saw this device.
func (c *delayCalibrator) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset()
	c.applied = 0
}

func (c *delayCalibrator) reset() {
	c.ref = c.ref[:0]
	c.mic = c.mic[:0]
	c.collecting = false
	c.complete = false
	c.sinceRef = 0
}
