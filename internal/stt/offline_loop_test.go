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

// busyFrames is a live test FrameSource that always has a non-speech data frame
// ready: it never reports ErrNoFrameReady and never sets Final, so a start
// timeout must be enforced by the main-loop endpoint decision rather than the
// no-frame-ready branch.
type busyFrames struct{}

func (busyFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	return audio.Frame{
		Samples:    make([]float32, 1600),
		SampleRate: audio.SampleRate,
		Channels:   1,
		SourceKind: audio.SourceLive,
	}, nil
}

func (busyFrames) Close() error { return nil }

// fakeSegmenter is a scriptable segmenter: speech toggles IsSpeech, segments is
// the mid-stream finalized queue, and flushSeg is appended on Flush (EOF).
type fakeSegmenter struct {
	speech    bool
	speechSeq []bool
	accepts   int
	segments  [][]float32
	flushSeg  []float32
}

func (f *fakeSegmenter) AcceptWaveform([]float32) {
	if len(f.speechSeq) > 0 {
		i := f.accepts
		if i >= len(f.speechSeq) {
			i = len(f.speechSeq) - 1
		}
		f.speech = f.speechSeq[i]
	}
	f.accepts++
}
func (f *fakeSegmenter) IsSpeech() bool    { return f.speech }
func (f *fakeSegmenter) HasSegments() bool { return len(f.segments) > 0 }

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

func TestOfflineLoopPolicyTrailingSilenceFinalizes(t *testing.T) {
	deps := offlineLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{
			make([]float32, 1600), // 100ms speech
			make([]float32, 1600), // 100ms trailing silence
		}},
		seg:        &fakeSegmenter{speechSeq: []bool{true, false}, flushSeg: longSpeech()},
		policy:     endpoint.Policy{MinSpeech: 100 * time.Millisecond, MinSilence: 100 * time.Millisecond},
		transcribe: func([]float32) (string, error) { return "silence finalized", nil },
	}

	got := runLoop(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "silence finalized" {
		t.Fatalf("FinalTranscript.Text = %q, want silence finalized", final.Text)
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

func TestOfflineLoopBusyNoSpeechStartTimeout(t *testing.T) {
	// A live source streaming non-speech frames back to back (never
	// ErrNoFrameReady, never Final) must still hit the no-speech start timeout
	// through the main-loop endpoint decision rather than listening forever. The
	// context deadline is only a safety net: on success the loop times out well
	// before it fires.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deps := offlineLoopDeps{
		frames:     busyFrames{},
		seg:        &fakeSegmenter{speech: false},
		policy:     endpoint.Policy{StartTimeout: time.Millisecond},
		transcribe: func([]float32) (string, error) { return "", nil },
	}

	got := runLoop(ctx, deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout (start timeout via main loop)", terminal(got))
	}
}

// TestOfflineLoopShortCommandFinalizesOnFrameEOF is the short-command regression
// guard for the frame-contract migration: a finite source that speaks only the
// frame contract (scriptedFrames implements FrameSource but not the legacy
// finiteAudioSource interface) must finalize promptly on its Final frame, with no
// spurious Timeout. The pre-migration loop detected EOF via sourceExhausted() and
// would not have recognized this source's end — it would wait for a timeout.
func TestOfflineLoopShortCommandFinalizesOnFrameEOF(t *testing.T) {
	deps := offlineLoopDeps{
		frames:     &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600), make([]float32, 1600)}},
		seg:        &fakeSegmenter{speech: true, flushSeg: longSpeech()},
		policy:     endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond},
		transcribe: func([]float32) (string, error) { return "hello samantha", nil },
	}

	got := runLoop(context.Background(), deps)
	for _, e := range got {
		if _, ok := e.(Timeout); ok {
			t.Fatalf("emitted Timeout for a short fixture command; events = %v", got)
		}
	}
	final, ok := terminal(got).(FinalTranscript)
	if !ok || final.Text != "hello samantha" {
		t.Fatalf("terminal = %v, want FinalTranscript{\"hello samantha\"}", terminal(got))
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
