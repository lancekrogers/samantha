package audio

import (
	"math"
	"sync"
)

const (
	frontendTargetRMS     = 0.08
	frontendMaxRefQueue   = SampleRate * 2
	echoCancellerTaps     = 192
	echoCancellerStep     = 0.08
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

type sampleQueue struct {
	samples  []float64
	capacity int
}

func (q *sampleQueue) push(samples []float32) {
	if len(samples) == 0 {
		return
	}

	for _, sample := range samples {
		q.samples = append(q.samples, float64(sample))
	}
	if len(q.samples) > q.capacity {
		q.samples = append([]float64(nil), q.samples[len(q.samples)-q.capacity:]...)
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
	q.samples = append([]float64(nil), q.samples[available:]...)
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
