package stt

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
)

// liveOnly implements only the legacy audioSource (no Exhausted), like live capture.
type liveOnly struct{}

func (liveOnly) Read() []float32 { return nil }

// finiteSrc implements the finite contract, like a fixture.
type finiteSrc struct{ done bool }

func (f *finiteSrc) Read() []float32 { return nil }
func (f *finiteSrc) Exhausted() bool { return f.done }

func TestSourceKindClassifiesLiveVsFinite(t *testing.T) {
	if got := sourceKind(liveOnly{}); got != audio.SourceLive {
		t.Errorf("sourceKind(live) = %q, want %q", got, audio.SourceLive)
	}
	if got := sourceKind(&finiteSrc{}); got != audio.SourceFixture {
		t.Errorf("sourceKind(finite) = %q, want %q", got, audio.SourceFixture)
	}
}

func TestSourceExhausted(t *testing.T) {
	if sourceExhausted(liveOnly{}) {
		t.Error("sourceExhausted(live) = true, want false (live never exhausts)")
	}

	f := &finiteSrc{done: false}
	if sourceExhausted(f) {
		t.Error("sourceExhausted(not-done) = true, want false")
	}
	f.done = true
	if !sourceExhausted(f) {
		t.Error("sourceExhausted(done) = false, want true")
	}
}
