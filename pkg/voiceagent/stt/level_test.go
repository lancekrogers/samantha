package stt

import (
	"testing"
	"time"
)

func TestFrameRMSAndNormalize(t *testing.T) {
	if frameRMS(nil) != 0 {
		t.Fatal("empty RMS must be 0")
	}
	silent := make([]float32, 100)
	if frameRMS(silent) != 0 {
		t.Fatal("silence RMS must be 0")
	}
	loud := make([]float32, 100)
	for i := range loud {
		loud[i] = 0.2
	}
	rms := frameRMS(loud)
	if rms < 0.19 || rms > 0.21 {
		t.Fatalf("rms = %v, want ~0.2", rms)
	}
	level := normalizeInputLevel(rms)
	if level != 1 {
		t.Fatalf("normalize(0.2) = %v, want 1 (clamped)", level)
	}
	soft := normalizeInputLevel(0.06)
	if soft <= 0 || soft >= 1 {
		t.Fatalf("normalize(0.06) = %v, want mid range", soft)
	}
}

func TestLevelEmitterThrottles(t *testing.T) {
	ch := make(chan Event, 8)
	var e levelEmitter
	samples := make([]float32, 64)
	for i := range samples {
		samples[i] = 0.1
	}
	e.maybeEmit(ch, samples)
	e.maybeEmit(ch, samples) // same window — should drop
	if len(ch) != 1 {
		t.Fatalf("emits = %d, want 1 after throttle", len(ch))
	}
	// Force next window.
	e.last = time.Now().Add(-levelEmitMinInterval * 2)
	e.maybeEmit(ch, samples)
	if len(ch) != 2 {
		t.Fatalf("emits = %d, want 2 after interval", len(ch))
	}
	ev := <-ch
	lvl, ok := ev.(InputLevel)
	if !ok || lvl.Level <= 0 {
		t.Fatalf("event = %#v, want InputLevel > 0", ev)
	}
}

func TestTrySendEventDropsWhenFull(t *testing.T) {
	ch := make(chan Event) // unbuffered
	// Non-blocking send into full/unbuffered channel without receiver.
	trySendEvent(ch, InputLevel{Level: 0.5})
	// If we get here without blocking, the drop path works.
}
