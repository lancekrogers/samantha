package audio

import (
	"encoding/binary"
	"math"
)

// AEC far-end ingress for the playback device callback.
//
// The miniaudio onData path must stay allocation-free and non-blocking: it only
// copies mono S16 into a pooled slot and does a non-blocking send. A worker
// resamples (streaming, phase-preserving) and pushes into the Frontend under
// its normal mutex — never contending with the device callback.

const (
	aecRefPoolSlots = 16
	// aecRefSlotCap is device-rate mono frames per slot. Period sizes are
	// typically 512; headroom covers larger backends without truncation.
	aecRefSlotCap = 4096
	// aecOutScratchCap is enough capture-rate samples for one max slot after
	// downsampling (e.g. 4096 @ 48 kHz → ~1365 @ 16 kHz) plus phase margin.
	aecOutScratchCap = aecRefSlotCap + 8
)

type aecRefSlot struct {
	samples []float32
	n       int
	rate    int
}

// aecRefIngress is the callback-safe → worker handoff for AEC reference audio.
type aecRefIngress struct {
	frontend Frontend
	free     chan *aecRefSlot
	ready    chan *aecRefSlot
	stop     chan struct{}
	done     chan struct{}
}

func newAECRefIngress(frontend Frontend) *aecRefIngress {
	in := &aecRefIngress{
		frontend: frontend,
		free:     make(chan *aecRefSlot, aecRefPoolSlots),
		ready:    make(chan *aecRefSlot, aecRefPoolSlots),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for range aecRefPoolSlots {
		in.free <- &aecRefSlot{samples: make([]float32, aecRefSlotCap)}
	}
	go in.loop()
	return in
}

// TryEnqueueS16 copies mono S16LE (device clock) into a free slot and hands it
// to the worker. Non-blocking: if the pool or ready queue is full, the block is
// dropped rather than stalling the device callback.
func (in *aecRefIngress) TryEnqueueS16(monoS16 []byte, deviceRate int) {
	if in == nil || len(monoS16) < 2 {
		return
	}
	frames := len(monoS16) / 2
	if frames > aecRefSlotCap {
		frames = aecRefSlotCap
	}

	var slot *aecRefSlot
	select {
	case slot = <-in.free:
	default:
		return
	}

	for i := 0; i < frames; i++ {
		s := int16(binary.LittleEndian.Uint16(monoS16[i*2:]))
		slot.samples[i] = float32(s) / float32(math.MaxInt16)
	}
	slot.n = frames
	slot.rate = deviceRate

	select {
	case in.ready <- slot:
	default:
		// Return the slot so the pool does not leak under sustained overload.
		select {
		case in.free <- slot:
		default:
		}
	}
}

func (in *aecRefIngress) loop() {
	defer close(in.done)
	var resamp streamResampler
	outScratch := make([]float32, aecOutScratchCap)

	for {
		select {
		case <-in.stop:
			in.drainReady()
			return
		case slot := <-in.ready:
			in.process(slot, &resamp, outScratch)
			select {
			case in.free <- slot:
			default:
			}
		}
	}
}

func (in *aecRefIngress) process(slot *aecRefSlot, resamp *streamResampler, outScratch []float32) {
	if slot.n <= 0 || in.frontend == nil {
		return
	}
	src := slot.samples[:slot.n]
	rate := slot.rate
	if rate <= 0 {
		rate = SampleRate
	}
	if rate == SampleRate {
		in.frontend.PushPlaybackReference(src)
		return
	}
	resamp.Configure(rate, SampleRate)
	// Worst-case output length for this block (ceil) plus a couple of samples.
	maxOut := (slot.n*SampleRate)/rate + 3
	if maxOut > len(outScratch) {
		maxOut = len(outScratch)
	}
	n := resamp.Resample(src, outScratch[:maxOut])
	if n > 0 {
		in.frontend.PushPlaybackReference(outScratch[:n])
	}
}

func (in *aecRefIngress) drainReady() {
	for {
		select {
		case slot := <-in.ready:
			select {
			case in.free <- slot:
			default:
			}
		default:
			return
		}
	}
}

// Close stops the worker and waits for it to exit. Safe to call once.
func (in *aecRefIngress) Close() {
	if in == nil {
		return
	}
	select {
	case <-in.stop:
		// already closed
	default:
		close(in.stop)
	}
	<-in.done
}
