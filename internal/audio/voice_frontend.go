package audio

import (
	"math"
	"sync"
)

const (
	frontendTargetRMS = 0.08
	// frontendMaxRefQueue holds far-end audio at the capture sample rate. The
	// reference FIFO has no independent notion of time: a backlog IS
	// misalignment, so the cap bounds how far out of alignment the canceller
	// can drift if the playback and capture clocks disagree. Steady state is
	// refs.delay (tens of ms); 2s is pure slack.
	frontendMaxRefQueue = SampleRate * 2
	// echoCancellerTaps must span the residual echo delay left after
	// refs.delay removes the bulk device-buffer latency — room reflections,
	// driver latency beyond the ring buffer, and error in the delay estimate.
	// Cancellation falls off a cliff the moment the true lag exceeds the tap
	// window (see TestEchoCancellerERLEAcrossDeviceLatency), so this is sized
	// for margin, not for the nominal case: 512 taps = 32ms @16kHz.
	echoCancellerTaps = 512
	// echoCancellerStep is the NLMS adaptation rate. 0.08 converged too slowly
	// to be useful on utterance-length audio once the filter grew long enough
	// to span the real echo delay (~6dB ERLE); 0.4 reaches ~10dB on the same
	// signal. Measured cost in double-talk is ~0.3dB of near-end preservation:
	// the |err| > 2.5*|estimated| guard in Process is what keeps the user's own
	// voice from being adapted away, and it still fires at this rate. Well
	// inside NLMS stability (step < 2).
	echoCancellerStep     = 0.4
	echoCancellerLeak     = 0.9995
	noiseSuppressorFloor  = 0.12
	noiseSuppressorTarget = 0.22
	agcMinGain            = 0.8
	agcMaxGain            = 6.0
	highPassAlpha         = 0.995
)

// VoiceFrontend applies local AEC/NS/AGC before VAD and STT.
type VoiceFrontend struct {
	mu sync.Mutex

	highPass highPassFilter
	aec      nlmsEchoCanceller
	ns       noiseSuppressor
	agc      automaticGainControl
	refs     sampleQueue
}

// NewVoiceFrontend creates the default local audio front-end.
func NewVoiceFrontend() *VoiceFrontend {
	return &VoiceFrontend{
		aec: newNLMSEchoCanceller(echoCancellerTaps, echoCancellerStep, echoCancellerLeak),
		ns:  newNoiseSuppressor(noiseSuppressorFloor, noiseSuppressorTarget),
		agc: newAutomaticGainControl(frontendTargetRMS, agcMinGain, agcMaxGain),
		refs: sampleQueue{
			capacity: frontendMaxRefQueue,
		},
	}
}

// ProcessCapture runs microphone audio through high-pass, AEC, noise suppression,
// and automatic gain control before it reaches VAD/STT.
func (f *VoiceFrontend) ProcessCapture(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	refs := f.refs.pop(len(samples))
	out := make([]float32, len(samples))

	for i, sample := range samples {
		clean := f.highPass.Process(float64(sample))
		echoFree := f.aec.Process(clean, refs[i])
		out[i] = float32(echoFree)
	}

	f.ns.Process(out)
	f.agc.Process(out)
	return out
}

// SetReferenceDelay declares how far behind the push the far-end audio actually
// becomes audible, in capture-rate samples. The player derives it from the
// device buffer it configured; see Player.applyReferenceDelay.
//
// Changing it resets the queue: the old backlog was built for the old offset,
// and holding it would misalign the first burst under the new one.
func (f *VoiceFrontend) SetReferenceDelay(samples int) {
	if samples < 0 {
		samples = 0
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs.delay == samples {
		return
	}
	f.refs.delay = samples
	f.refs.samples = f.refs.samples[:0]
}

// PushPlaybackReference feeds far-end playback audio into the AEC reference path.
func (f *VoiceFrontend) PushPlaybackReference(samples []float32) {
	if len(samples) == 0 {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs.push(samples)
}

// Close releases front-end resources.
func (f *VoiceFrontend) Close() error {
	return nil
}

type highPassFilter struct {
	lastX float64
	lastY float64
}

func (h *highPassFilter) Process(sample float64) float64 {
	y := highPassAlpha * (h.lastY + sample - h.lastX)
	h.lastX = sample
	h.lastY = y
	return y
}

type nlmsEchoCanceller struct {
	coeffs []float64
	hist   []float64
	pos    int
	step   float64
	leak   float64
}

func newNLMSEchoCanceller(taps int, step, leak float64) nlmsEchoCanceller {
	return nlmsEchoCanceller{
		coeffs: make([]float64, taps),
		hist:   make([]float64, taps),
		step:   step,
		leak:   leak,
	}
}

func (n *nlmsEchoCanceller) Process(mic, ref float64) float64 {
	if len(n.hist) == 0 {
		return mic
	}

	n.hist[n.pos] = ref
	n.pos = (n.pos + 1) % len(n.hist)

	estimated := 0.0
	energy := 1e-6
	for i := range n.coeffs {
		x := n.hist[(n.pos-1-i+len(n.hist))%len(n.hist)]
		estimated += n.coeffs[i] * x
		energy += x * x
	}

	err := mic - estimated

	adaptScale := n.step / energy
	if math.Abs(err) > math.Abs(estimated)*2.5 {
		adaptScale *= 0.2
	}

	for i := range n.coeffs {
		x := n.hist[(n.pos-1-i+len(n.hist))%len(n.hist)]
		n.coeffs[i] = n.coeffs[i]*n.leak + adaptScale*err*x
	}

	return err
}

type noiseSuppressor struct {
	noiseFloor float64
	targetSNR  float64
}

func newNoiseSuppressor(initialFloor, targetSNR float64) noiseSuppressor {
	return noiseSuppressor{
		noiseFloor: initialFloor,
		targetSNR:  targetSNR,
	}
}

func (n *noiseSuppressor) Process(samples []float32) {
	if len(samples) == 0 {
		return
	}

	rms := frameRMS(samples)
	if rms < n.noiseFloor*1.8 {
		n.noiseFloor = 0.992*n.noiseFloor + 0.008*rms
	} else {
		n.noiseFloor = 0.999*n.noiseFloor + 0.001*rms
	}

	signal := rms
	noise := math.Max(n.noiseFloor, 1e-4)
	snr := signal / noise
	gain := clampFloat((snr-1.0)/math.Max(n.targetSNR, 1.0), noiseSuppressorFloor, 1.0)
	gate := math.Max(n.noiseFloor*1.6, 0.0015)

	for i, sample := range samples {
		value := float64(sample) * gain
		if math.Abs(value) < gate {
			value *= 0.12
		}
		samples[i] = float32(value)
	}
}

type automaticGainControl struct {
	target float64
	min    float64
	max    float64
	gain   float64
}

func newAutomaticGainControl(target, minGain, maxGain float64) automaticGainControl {
	return automaticGainControl{
		target: target,
		min:    minGain,
		max:    maxGain,
		gain:   1.0,
	}
}

func (a *automaticGainControl) Process(samples []float32) {
	if len(samples) == 0 {
		return
	}

	rms := frameRMS(samples)
	targetGain := clampFloat(a.target/math.Max(rms, 1e-4), a.min, a.max)
	if targetGain > a.gain {
		a.gain = 0.3*a.gain + 0.7*targetGain
	} else {
		a.gain = 0.92*a.gain + 0.08*targetGain
	}

	for i, sample := range samples {
		samples[i] = float32(clampFloat(float64(sample)*a.gain, -1.0, 1.0))
	}
}

// sampleQueue is the AEC far-end FIFO. ProcessCapture pops exactly as many
// reference samples as it has mic samples, so position in the queue is the only
// clock: reference sample k is paired with mic sample k.
//
// delay is what makes that pairing physically true. onData enqueues frames when
// it *fills* the device buffer, but those frames are not audible until the
// buffer drains — Periods*PeriodSizeInFrames, plus driver and acoustic latency.
// Without an offset the canceller is handed the far-end audio tens of
// milliseconds before its echo reaches the mic, which is outside the filter's
// tap window, and ERLE collapses to ~0dB no matter how well the rates line up.
type sampleQueue struct {
	samples  []float64
	capacity int
	// delay is the standing backlog, in capture-rate samples, injected at the
	// start of each playback burst so pops trail pushes by the output latency.
	delay int
}

func (q *sampleQueue) push(samples []float32) {
	if len(samples) == 0 {
		return
	}

	// A burst starts with the queue drained (silence pops it empty). Prime the
	// offset here rather than tracking playback state: these zeros are handed
	// out while the audio is still inside the device buffer and genuinely
	// inaudible, so a silent reference is the correct answer for that window.
	// The resulting backlog also absorbs callback jitter, which is what keeps
	// the queue from re-priming mid-burst.
	if len(q.samples) == 0 && q.delay > 0 {
		q.samples = append(q.samples, make([]float64, q.delay)...)
	}

	for _, sample := range samples {
		q.samples = append(q.samples, float64(sample))
	}
	if len(q.samples) > q.capacity {
		// Drop from the front; copy in place rather than reallocating the
		// whole backlog on every push once the cap is reached.
		excess := len(q.samples) - q.capacity
		copy(q.samples, q.samples[excess:])
		q.samples = q.samples[:q.capacity]
	}
}

func (q *sampleQueue) pop(n int) []float64 {
	if n <= 0 {
		return nil
	}

	out := make([]float64, n)
	if len(q.samples) == 0 {
		return out
	}

	available := min(len(q.samples), n)
	copy(out, q.samples[:available])
	copy(q.samples, q.samples[available:])
	q.samples = q.samples[:len(q.samples)-available]
	return out
}

func frameRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}

	sum := 0.0
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// refsSnapshot exposes the pending reference queue for tests that assert on
// the delay offset. Not part of the Frontend contract.
func (f *VoiceFrontend) refsSnapshot() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.refs.samples...)
}
