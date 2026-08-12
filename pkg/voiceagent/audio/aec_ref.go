package audio

import (
	"encoding/binary"
	"math"
	"sync/atomic"
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
	// gapBefore is capture-rate samples of far-end audio dropped immediately
	// ahead of this slot, which the worker replaces with silence so the block
	// lands at its true position in the far-end timeline.
	gapBefore int
}

// aecRefIngress is the callback-safe → worker handoff for AEC reference audio.
type aecRefIngress struct {
	frontend Frontend
	free     chan *aecRefSlot
	ready    chan *aecRefSlot
	stop     chan struct{}
	done     chan struct{}

	// pendingGap accumulates capture-rate samples of far-end audio the callback
	// had to drop, and rides along on the next slot that does get through. The
	// worker replaces it with silence so the FIFO's depth keeps meaning "this
	// much time behind the push". Without it a drop shifts every later sample
	// earlier by the lost amount, walking the true echo out of the tap window —
	// the same self-barge failure this path exists to fix, triggered by
	// overload rather than by finalizeSegment. Carrying it on the slot rather
	// than in a shared counter keeps the silence in stream order: blocks already
	// queued ahead of the drop must not have it spliced in front of them.
	pendingGap atomic.Int64
	dropped    atomic.Uint64
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

// TryEnqueueS16 copies mono S16LE (device clock) into free slots and hands them
// to the worker. Non-blocking: if the pool or ready queue is full, the rest of
// the block is dropped rather than stalling the device callback, and the lost
// duration is recorded as a gap so the worker can keep the timeline aligned.
//
// A callback larger than one slot is split across slots rather than truncated:
// silently discarding the tail would skew the far-end timeline for the whole
// utterance on any backend that hands us more than aecRefSlotCap frames.
func (in *aecRefIngress) TryEnqueueS16(monoS16 []byte, deviceRate int) {
	if in == nil || len(monoS16) < 2 {
		return
	}
	if deviceRate <= 0 {
		deviceRate = SampleRate
	}

	total := len(monoS16) / 2
	for off := 0; off < total; off += aecRefSlotCap {
		frames := min(total-off, aecRefSlotCap)
		if !in.enqueueBlock(monoS16[off*2:], frames, deviceRate) {
			// The pool is starved; the rest of this callback is lost with it.
			in.recordGap(total-off, deviceRate)
			return
		}
	}
}

// enqueueBlock fills one slot and hands it off, reporting whether it landed.
// The slot carries any gap accumulated since the last successful handoff.
func (in *aecRefIngress) enqueueBlock(monoS16 []byte, frames, deviceRate int) bool {
	var slot *aecRefSlot
	select {
	case slot = <-in.free:
	default:
		return false
	}

	for i := 0; i < frames; i++ {
		s := int16(binary.LittleEndian.Uint16(monoS16[i*2:]))
		slot.samples[i] = float32(s) / float32(math.MaxInt16)
	}
	slot.n = frames
	slot.rate = deviceRate
	slot.gapBefore = int(in.pendingGap.Swap(0))

	select {
	case in.ready <- slot:
		return true
	default:
		// Return the slot so the pool does not leak under sustained overload,
		// and put the gap back so the next successful block still carries it.
		in.pendingGap.Add(int64(slot.gapBefore))
		slot.gapBefore = 0
		select {
		case in.free <- slot:
		default:
		}
		return false
	}
}

// recordGap converts dropped device frames to capture-rate samples the worker
// must replace with silence.
func (in *aecRefIngress) recordGap(frames, deviceRate int) {
	in.dropped.Add(1)
	in.pendingGap.Add(int64(frames) * int64(SampleRate) / int64(deviceRate))
}

// droppedBlocks reports how many far-end blocks the device callback could not
// hand off. Non-zero means the AEC ran on a gap-filled reference; sustained
// growth is the signal to look at worker scheduling before blaming the filter.
func (in *aecRefIngress) droppedBlocks() uint64 {
	if in == nil {
		return 0
	}
	return in.dropped.Load()
}

func (in *aecRefIngress) loop() {
	defer close(in.done)
	var resamp streamResampler
	outScratch := make([]float32, aecOutScratchCap)
	silence := make([]float32, aecOutScratchCap)

	for {
		select {
		case <-in.stop:
			in.drainReady()
			return
		case slot := <-in.ready:
			in.fillGap(slot.gapBefore, silence)
			in.process(slot, &resamp, outScratch)
			select {
			case in.free <- slot:
			default:
			}
		}
	}
}

// fillGap pushes silence for far-end audio the callback dropped, so the FIFO's
// backlog still measures the output latency. Silence is the honest filler: the
// audio is genuinely unknown, and an all-zero reference block contributes no
// NLMS adaptation rather than teaching the filter something wrong.
func (in *aecRefIngress) fillGap(gap int, silence []float32) {
	if gap <= 0 || in.frontend == nil {
		return
	}
	// Beyond the FIFO cap the queue drops from the front anyway, so a longer
	// fill buys nothing and costs a full re-walk of the backlog.
	gap = min(gap, frontendMaxRefQueue)
	for gap > 0 {
		n := min(gap, len(silence))
		in.frontend.PushPlaybackReference(silence[:n])
		gap -= n
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
