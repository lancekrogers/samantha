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

// finalizeSegment must not bulk-push the utterance into the AEC queue — that
// path drained the reference before speakers emitted and barge-in heard self.
func TestFinalizeSegmentDoesNotPushAECReference(t *testing.T) {
	seg := newPlaybackSegment()
	rec := &recordingFrontend{}
	samples := make([]float32, 4800) // 200ms @ 24k
	for i := range samples {
		samples[i] = 0.25
	}
	finalizeSegment(seg, rec, nil, samples, 24000, 24000, nil)
	if got := rec.totalRefSamples(); got != 0 {
		t.Fatalf("finalizeSegment pushed %d ref samples; want 0 (onData/aecIngress owns AEC feed)", got)
	}
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
