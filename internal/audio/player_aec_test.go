package audio

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"
)

// recordingFrontend captures PushPlaybackReference calls for AEC wiring tests.
type recordingFrontend struct {
	mu   sync.Mutex
	refs [][]float32
}

func (r *recordingFrontend) ProcessCapture(samples []float32) []float32 { return samples }
func (r *recordingFrontend) Close() error                               { return nil }
func (r *recordingFrontend) PushPlaybackReference(samples []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := append([]float32(nil), samples...)
	r.refs = append(r.refs, cp)
}

func (r *recordingFrontend) totalRefSamples() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.refs {
		n += len(s)
	}
	return n
}

// finalizeSegment must not touch the AEC queue — bulk-pushing the utterance
// there drained the reference before the speakers emitted and barge-in heard
// self. It no longer takes a Frontend at all, so that is now a compile-time
// property; what still needs asserting is that dropping the parameter did not
// cost the segment its audio.
func TestFinalizeSegmentStillDeliversAudio(t *testing.T) {
	seg := newPlaybackSegment()
	samples := make([]float32, 4800) // 200ms @ 24k
	for i := range samples {
		samples[i] = 0.25
	}
	finalizeSegment(seg, nil, samples, 24000, 24000, nil)
	if !segmentReady(seg) {
		t.Fatal("segment not ready after finalizeSegment")
	}
}

// referenceDelaySamples must stay strictly under the true output latency: the
// canceller can spend taps covering an under-estimate, but an over-estimate
// asks it to predict the mic from reference audio that has not been pushed yet.
func TestReferenceDelayStaysUnderDeviceBuffer(t *testing.T) {
	for _, deviceRate := range []int{44100, 48000} {
		got := referenceDelaySamples(deviceRate)
		ringSamples := playbackPeriods * playbackPeriodFrames * SampleRate / deviceRate
		if got <= 0 {
			t.Fatalf("referenceDelaySamples(%d) = %d, want > 0", deviceRate, got)
		}
		if got >= ringSamples {
			t.Fatalf("referenceDelaySamples(%d) = %d, must stay under the %d-sample ring buffer",
				deviceRate, got, ringSamples)
		}
		if got >= echoCancellerTaps*2 {
			t.Fatalf("referenceDelaySamples(%d) = %d; residual would exceed the %d-tap window",
				deviceRate, got, echoCancellerTaps)
		}
	}
	if got := referenceDelaySamples(0); got != 0 {
		t.Fatalf("referenceDelaySamples(0) = %d, want 0", got)
	}
}

// delayFrontend records SetReferenceDelay for the wiring test.
type delayFrontend struct {
	recordingFrontend
	mu    sync.Mutex
	delay int
	calls int
}

func (d *delayFrontend) SetReferenceDelay(samples int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delay = samples
	d.calls++
}

func (d *delayFrontend) snapshot() (delay, calls int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.delay, d.calls
}

// Production registers the front-end at startup but does not open the playback
// device until the first utterance, so the reference delay cannot be known at
// SetFrontend time. It has to land once the device rate is negotiated —
// otherwise every session runs uncompensated.
func TestReferenceDelayAppliedWhenDeviceRateArrivesLate(t *testing.T) {
	fe := &delayFrontend{}
	p := &Player{}

	// SetFrontend before any device: nothing sensible to publish yet.
	p.frontend = fe
	p.applyReferenceDelay()
	if delay, calls := fe.snapshot(); calls != 0 || delay != 0 {
		t.Fatalf("delay published with no device: delay=%d calls=%d", delay, calls)
	}

	// ensureDevice negotiates a rate and re-publishes.
	p.sampleRate = 48000
	p.applyReferenceDelay()
	delay, calls := fe.snapshot()
	if calls != 1 {
		t.Fatalf("SetReferenceDelay called %d times, want 1 once the rate is known", calls)
	}
	if want := referenceDelaySamples(48000); delay != want {
		t.Fatalf("published delay = %d, want %d", delay, want)
	}
}

// A front-end that does not implement ReferenceDelayer (passthrough, doubles)
// must not be a problem for the player.
func TestReferenceDelaySkipsNonDelayerFrontend(t *testing.T) {
	p := &Player{frontend: NewPassthroughFrontend(), sampleRate: 48000}
	p.applyReferenceDelay() // must not panic
}

func TestAECRefIngressResamplesToCaptureRate(t *testing.T) {
	rec := &recordingFrontend{}
	in := newAECRefIngress(rec)
	defer in.Close()

	const frames = 480 // 10ms @ 48 kHz
	mono := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		mono[i*2] = 0x00
		mono[i*2+1] = 0x20
	}
	in.TryEnqueueS16(mono, 48000)

	// Worker is async; wait briefly for the push.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		// 480 @ 48k → 160 @ 16k
		if got := rec.totalRefSamples(); got >= 150 && got <= 170 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ref samples = %d, want ~160 at 16 kHz capture rate", rec.totalRefSamples())
}

// TestStreamResamplerPreservesPhaseAcrossCallbacks is the multi-callback
// regression for AEC alignment: independent per-callback resample of 512
// frames @ 48 kHz yields 171 samples each time (round), accumulating ~31
// extra 16 kHz samples per second. Streaming must stay near the true rate.
func TestStreamResamplerPreservesPhaseAcrossCallbacks(t *testing.T) {
	const (
		srcRate   = 48000
		dstRate   = 16000
		cbFrames  = 512
		callbacks = 100 // ~1.067s of device audio
	)
	var r streamResampler
	r.Configure(srcRate, dstRate)

	in := make([]float32, cbFrames)
	for i := range in {
		in[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / float64(srcRate)))
	}
	outScratch := make([]float32, cbFrames) // plenty for downsampling

	totalOut := 0
	for range callbacks {
		totalOut += r.Resample(in, outScratch)
	}

	// True expected: callbacks * cbFrames * dst/src
	want := float64(callbacks*cbFrames) * float64(dstRate) / float64(srcRate)
	// Allow ±2 samples of total phase error over ~1s (stateless was ~+33).
	if math.Abs(float64(totalOut)-want) > 2 {
		t.Fatalf("stream resampler produced %d samples over %d callbacks, want ~%.1f (±2)", totalOut, callbacks, want)
	}

	// Contrast: stateless per-callback resample drifts hard.
	stateless := 0
	for range callbacks {
		stateless += len(resampleLinear(in, srcRate, dstRate))
	}
	if math.Abs(float64(stateless)-want) <= 2 {
		t.Fatalf("stateless resample unexpectedly aligned (got %d, want drift vs %.1f); test is not discriminating", stateless, want)
	}
}

func TestAECRefIngressTryEnqueueIsNonBlocking(t *testing.T) {
	// A frontend that blocks on Push would hang a naive synchronous path;
	// the ingress worker absorbs that. Enqueue itself must not block.
	block := make(chan struct{})
	fe := &blockingFrontend{block: block}
	in := newAECRefIngress(fe)
	defer func() {
		close(block)
		in.Close()
	}()

	mono := make([]byte, 64)
	for i := 0; i < 32; i++ {
		binary.LittleEndian.PutUint16(mono[i*2:], 0x1000)
	}
	done := make(chan struct{})
	go func() {
		// Flood the pool; must return promptly even if worker is stuck on Push.
		for range aecRefPoolSlots * 4 {
			in.TryEnqueueS16(mono, SampleRate)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("TryEnqueueS16 blocked; callback path must be non-blocking")
	}
}

type blockingFrontend struct {
	block <-chan struct{}
}

func (b *blockingFrontend) ProcessCapture(s []float32) []float32 { return s }
func (b *blockingFrontend) Close() error                         { return nil }
func (b *blockingFrontend) PushPlaybackReference([]float32) {
	<-b.block
}
