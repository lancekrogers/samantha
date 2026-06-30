package stt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// scriptedFrames is a finite test FrameSource: it emits the given chunks as
// frames, then one Final frame, then ErrSourceClosed.
type scriptedFrames struct {
	chunks [][]float32
	i      int
	done   bool
}

func (s *scriptedFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	if s.i < len(s.chunks) {
		c := s.chunks[s.i]
		s.i++
		return audio.Frame{Samples: c, SampleRate: audio.SampleRate, Channels: audio.Channels, SourceKind: audio.SourceFixture}, nil
	}
	if !s.done {
		s.done = true
		return audio.Frame{SourceKind: audio.SourceFixture, Final: true}, nil
	}
	return audio.Frame{}, audio.ErrSourceClosed
}

func (s *scriptedFrames) Close() error { return nil }

// noFrames is a live test FrameSource that never has audio ready.
type noFrames struct{}

func (noFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	return audio.Frame{}, audio.ErrNoFrameReady
}

func (noFrames) Close() error { return nil }

// fakeSegmenter is a scriptable segmenter: speech toggles IsSpeech, segments is
// the mid-stream finalized queue, and flushSeg is appended on Flush (EOF).
type fakeSegmenter struct {
	speech   bool
	segments [][]float32
	flushSeg []float32
}

func (f *fakeSegmenter) AcceptWaveform([]float32) {}
func (f *fakeSegmenter) IsSpeech() bool           { return f.speech }
func (f *fakeSegmenter) HasSegments() bool        { return len(f.segments) > 0 }

func (f *fakeSegmenter) NextSegment() ([]float32, bool) {
	if len(f.segments) == 0 {
		return nil, false
	}
	seg := f.segments[0]
	f.segments = f.segments[1:]
	return seg, true
}

func (f *fakeSegmenter) Reset() {}
func (f *fakeSegmenter) Flush() {
	if f.flushSeg != nil {
		f.segments = append(f.segments, f.flushSeg)
		f.flushSeg = nil
	}
}

func runLoop(ctx context.Context, deps offlineLoopDeps) []Event {
	events := make(chan Event, 64)
	go func() {
		runOfflineLoop(ctx, deps, events)
		close(events)
	}()
	var got []Event
	for e := range events {
		got = append(got, e)
	}
	return got
}

func terminal(events []Event) Event {
	if len(events) == 0 {
		return nil
	}
	return events[len(events)-1]
}

func longSpeech() []float32 { return make([]float32, minSpeechSamples) }

func TestOfflineLoopFiniteEOFFinalizes(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: true, flushSeg: longSpeech()},
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { return "hello samantha", nil },
	}

	got := runLoop(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T (%v), want FinalTranscript", terminal(got), got)
	}
	if final.Text != "hello samantha" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "hello samantha")
	}
}

func TestOfflineLoopFiniteEOFTooShortTimesOut(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: true, flushSeg: make([]float32, 100)}, // < minSpeechSamples
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { t.Fatal("transcribe called for too-short speech"); return "", nil },
	}

	got := runLoop(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout", terminal(got))
	}
}

func TestOfflineLoopFiniteEOFNoSpeechTimesOut(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: false}, // no speech, no flush segment
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { t.Fatal("transcribe called with no speech"); return "", nil },
	}

	got := runLoop(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout", terminal(got))
	}
}

func TestOfflineLoopMidStreamSegmentFinalizes(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600), make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: true, segments: [][]float32{longSpeech()}}, // available immediately
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { return "what time is it", nil },
	}

	got := runLoop(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "what time is it" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "what time is it")
	}
}

func TestOfflineLoopCancellationFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:        &fakeSegmenter{},
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { return "", nil },
	}

	got := runLoop(ctx, deps)
	fail, ok := terminal(got).(Failure)
	if !ok {
		t.Fatalf("terminal event = %T, want Failure", terminal(got))
	}
	if !errors.Is(fail.Err, context.Canceled) {
		t.Errorf("Failure.Err = %v, want context.Canceled", fail.Err)
	}
}

func TestOfflineLoopLiveNoSpeechStartTimeout(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     noFrames{},
		seg:        &fakeSegmenter{speech: false},
		policy:     endpoint.Policy{StartTimeout: time.Millisecond},
		transcribe: func([]float32) (string, error) { return "", nil },
	}

	got := runLoop(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout", terminal(got))
	}
}

func TestOfflineLoopTranscribeErrorFails(t *testing.T) {
	wantErr := errors.New("decode failed")
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: true, flushSeg: longSpeech()},
		policy:     endpoint.Policy{},
		transcribe: func([]float32) (string, error) { return "", wantErr },
	}

	got := runLoop(context.Background(), deps)
	fail, ok := terminal(got).(Failure)
	if !ok {
		t.Fatalf("terminal event = %T, want Failure", terminal(got))
	}
	if !errors.Is(fail.Err, wantErr) {
		t.Errorf("Failure.Err = %v, want %v", fail.Err, wantErr)
	}
}
