package audio

import (
	"sync"
	"testing"
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
		t.Fatalf("finalizeSegment pushed %d ref samples; want 0 (onData owns AEC feed)", got)
	}
}

func TestPushPlaybackReferenceForCaptureResamplesToMicRate(t *testing.T) {
	rec := &recordingFrontend{}
	// 100 mono S16 samples at 48 kHz → should become ~33 samples at 16 kHz.
	const frames = 100
	mono := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		// modest amplitude
		mono[i*2] = 0x00
		mono[i*2+1] = 0x20
	}
	pushPlaybackReferenceForCapture(rec, mono, 48000)
	got := rec.totalRefSamples()
	// linear resample length = frames * 16000 / 48000 ≈ 33
	if got < 30 || got > 36 {
		t.Fatalf("ref samples = %d, want ~33 at 16 kHz capture rate", got)
	}
}
