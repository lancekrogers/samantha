package stt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/endpoint"
)

// fakeStreamingRec is a scriptable streamingRecognizer.
type fakeStreamingRec struct {
	partials      []string // one per Accept (clamped to the last entry)
	accepts       int
	endpointAfter int      // IsEndpoint() is true once accepts >= this (0 = never)
	finals        []string // consumed in order, one per Finalize() call
	finalIdx      int
	resets        int
}

func (f *fakeStreamingRec) Accept([]float32) { f.accepts++ }

func (f *fakeStreamingRec) Partial() string {
	i := f.accepts - 1
	if i < 0 {
		return ""
	}
	if i < len(f.partials) {
		return f.partials[i]
	}
	if len(f.partials) > 0 {
		return f.partials[len(f.partials)-1]
	}
	return ""
}

func (f *fakeStreamingRec) IsEndpoint() bool {
	return f.endpointAfter > 0 && f.accepts >= f.endpointAfter
}

func (f *fakeStreamingRec) Finalize() string {
	if f.finalIdx < len(f.finals) {
		v := f.finals[f.finalIdx]
		f.finalIdx++
		return v
	}
	if len(f.finals) > 0 {
		return f.finals[len(f.finals)-1]
	}
	return ""
}

func (f *fakeStreamingRec) Reset() { f.resets++ }

func runStreaming(ctx context.Context, deps streamingLoopDeps) []Event {
	events := make(chan Event, 128)
	go func() {
		runStreamingLoop(ctx, deps, events)
		close(events)
	}()
	var got []Event
	for e := range events {
		got = append(got, e)
	}
	return got
}

func countPartials(events []Event) int {
	n := 0
	for _, e := range events {
		if _, ok := e.(PartialTranscript); ok {
			n++
		}
	}
	return n
}

func TestStreamingLoopEOFFinalizesWithPartials(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600), make([]float32, 1600)}},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"hello", "hello samantha", "hello samantha"}, finals: []string{"hello samantha"}},
		policy: endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	if countPartials(got) < 1 {
		t.Errorf("partials emitted = %d, want >= 1", countPartials(got))
	}
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "hello samantha" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "hello samantha")
	}
}

func TestStreamingLoopProviderEndpointFinalizes(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600), make([]float32, 1600), make([]float32, 1600), make([]float32, 1600)}},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"a", "ab"}, endpointAfter: 2, finals: []string{"done"}},
		policy: endpoint.Policy{AllowProviderEnd: true, MinSpeech: 100 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "done" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "done")
	}
}

func TestStreamingLoopNoSpeechTimesOut(t *testing.T) {
	deps := streamingLoopDeps{
		frames: noFrames{},
		seg:    &fakeSegmenter{speech: false},
		rec:    &fakeStreamingRec{},
		policy: endpoint.Policy{StartTimeout: time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout", terminal(got))
	}
}

func TestStreamingLoopCancellationFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:    &fakeSegmenter{},
		rec:    &fakeStreamingRec{},
		policy: endpoint.Policy{},
	}

	got := runStreaming(ctx, deps)
	fail, ok := terminal(got).(Failure)
	if !ok {
		t.Fatalf("terminal event = %T, want Failure", terminal(got))
	}
	if !errors.Is(fail.Err, context.Canceled) {
		t.Errorf("Failure.Err = %v, want context.Canceled", fail.Err)
	}
}

func TestStreamingLoopEmptyFinalizeResetsThenRecovers(t *testing.T) {
	rec := &fakeStreamingRec{
		partials:      []string{"x"},
		endpointAfter: 1,
		finals:        []string{"", "recovered"}, // first finalize empty -> reset, second succeeds
	}
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600), make([]float32, 1600), make([]float32, 1600)}},
		seg:    &fakeSegmenter{speech: true},
		rec:    rec,
		policy: endpoint.Policy{AllowProviderEnd: true, MinSpeech: 50 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "recovered" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "recovered")
	}
	if rec.resets != 1 {
		t.Errorf("recognizer resets = %d, want 1", rec.resets)
	}
}
