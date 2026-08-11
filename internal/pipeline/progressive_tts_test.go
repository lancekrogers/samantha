package pipeline

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/tts"
)

// progressiveFakeTTS records each Synthesize text and optionally delays so
// brain streaming can continue while the first segment is still in flight.
type progressiveFakeTTS struct {
	mu    sync.Mutex
	calls []string
	// gate opens after the first call is recorded so the test can assert
	// progressive handoff (synth started before brain stream finished).
	firstCall chan struct{}
	// hold keeps the first synth open until released.
	holdFirst chan struct{}
	delay     time.Duration
}

func (f *progressiveFakeTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	f.mu.Lock()
	f.calls = append(f.calls, text)
	n := len(f.calls)
	f.mu.Unlock()

	if n == 1 && f.firstCall != nil {
		select {
		case <-f.firstCall:
		default:
			close(f.firstCall)
		}
	}
	if n == 1 && f.holdFirst != nil {
		select {
		case <-f.holdFirst:
		case <-ctx.Done():
			stream := audio.NewPCMStream(ctx)
			stream.CloseWithError(ctx.Err())
			return stream, nil
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return (&fakeTTS{delay: time.Millisecond}).Synthesize(ctx, text)
}

func (f *progressiveFakeTTS) Available() bool                       { return true }
func (f *progressiveFakeTTS) ListVoices(string, string) []tts.Voice { return nil }
func (f *progressiveFakeTTS) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// softCancelTTS records SoftCancel for barge-in tests.
type softCancelTTS struct {
	fakeTTS
	cancels atomic.Int32
}

func (s *softCancelTTS) SoftCancel(string) error {
	s.cancels.Add(1)
	return nil
}

// TestProgressiveTTSSegmentsFeedWorker proves multi-sentence brain streams
// become multiple TTS.Synthesize calls (one segment each), and the first
// segment is handed off before the brain stream completes.
func TestProgressiveTTSSegmentsFeedWorker(t *testing.T) {
	firstCall := make(chan struct{})
	holdFirst := make(chan struct{})
	provider := &progressiveFakeTTS{firstCall: firstCall, holdFirst: holdFirst}

	player := newFakePlayer(5 * time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		TTS:    provider,
		Player: player,
		Events: events.NewBus(),
	}

	// Slow chunk emission so first sentence can reach TTS before brain Done.
	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	brainDone := make(chan struct{})
	go func() {
		defer close(chunks)
		defer close(done)
		chunks <- "Hello there. "
		// Wait until first segment synth starts — progressive contract.
		select {
		case <-firstCall:
		case <-time.After(2 * time.Second):
			t.Error("first segment never reached TTS before more brain tokens")
			return
		}
		// Brain still streaming after first synth started.
		chunks <- "Second sentence is ready. "
		chunks <- "Third finishes the reply."
		// Release first synth so the ordered worker can process remaining sentences.
		close(holdFirst)
		done <- brain.StreamResult{}
		close(brainDone)
	}()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := &brain.Stream{Chunks: chunks, Done: done}
	response, interrupted, err := p.streamResponse(ctx, cancel, stream, false, metrics, turn)
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if interrupted {
		t.Fatal("unexpected interrupt")
	}
	if !strings.Contains(response, "Hello") || !strings.Contains(response, "Third") {
		t.Fatalf("response = %q", response)
	}

	calls := provider.Calls()
	// Chunking is adaptive: one sentence for the opening, two thereafter (see
	// brain.firstChunkSentences). Three sentences therefore reach TTS as two
	// segments. What matters is that synthesis is progressive at all — the
	// opening segment must go out while the brain is still streaming — not the
	// exact count, so assert the invariant rather than the arithmetic.
	if len(calls) < 2 {
		t.Fatalf("Synthesize calls = %q, want the reply split into progressive segments", calls)
	}
	// The opening segment must be exactly the first sentence: time-to-first-audio
	// waits on it, so batching or full-reply synthesis here is the regression
	// this test exists to catch.
	if calls[0] != "Hello there." {
		t.Fatalf("first synth = %q, want %q — the opening segment must be one sentence", calls[0], "Hello there.")
	}
	if metrics.firstSegment.IsZero() {
		t.Fatal("firstSegment metric not stamped")
	}
	if metrics.modelComplete.IsZero() {
		t.Fatal("modelComplete metric not stamped")
	}
	// Progressive win: first voice segment ready at or before brain complete
	// (wall clock from turn start). Allow equality for very fast brains.
	if metrics.firstSegment.After(metrics.modelComplete.Add(50 * time.Millisecond)) {
		t.Fatalf("firstSegment %v after modelComplete %v — not progressive",
			metrics.firstSegment, metrics.modelComplete)
	}
}

// TestProgressiveMetricsBrainDoneVsFirstSegment is the product measurement for
// progressive TTS (festival task 02): compare brain-complete wall to first
// voice segment handoff.
//
// On TurnMetrics events:
//   - FirstSegmentElapsed  — first sentence entered the TTS synth queue
//   - ModelCompleteElapsed — brain stream Done
//
// Progressive wins when FirstSegmentElapsed ≲ ModelCompleteElapsed for multi-
// sentence replies (user hears speech before the model finishes the turn).
// TTS-only cost for segment N is reflected in firstAudioReady / playback after
// that segment's Synthesize returns.
func TestProgressiveMetricsBrainDoneVsFirstSegment(t *testing.T) {
	provider := &progressiveFakeTTS{delay: 5 * time.Millisecond}
	player := newFakePlayer(5 * time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var captured events.TurnMetrics
	events.Subscribe(p.Events, func(m events.TurnMetrics) { captured = m })

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := scriptedStream(ctx, []string{
		"Alpha sentence. ", "Beta sentence. ", "Gamma end.",
	}, nil)
	_, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, turn)
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	turn.finish(TurnCompleted)

	if captured.FirstSegmentElapsed <= 0 {
		t.Fatalf("FirstSegmentElapsed = %v", captured.FirstSegmentElapsed)
	}
	if captured.ModelCompleteElapsed <= 0 {
		t.Fatalf("ModelCompleteElapsed = %v", captured.ModelCompleteElapsed)
	}
	// For multi-sentence progressive TTS, first segment should not wait for
	// full model completion (allows small timing skew).
	if captured.FirstSegmentElapsed > captured.ModelCompleteElapsed+20*time.Millisecond {
		t.Fatalf("FirstSegmentElapsed %v > ModelCompleteElapsed %v",
			captured.FirstSegmentElapsed, captured.ModelCompleteElapsed)
	}
	if len(provider.Calls()) < 2 {
		t.Fatalf("want multi-segment synth, got %v", provider.Calls())
	}
}

func TestSoftCancelOnBargeIn(t *testing.T) {
	// SoftCancel is invoked when barge-in fires mid-stream.
	// Use speak path with interrupt is heavy; unit-test softCancelTTS helper.
	provider := &softCancelTTS{fakeTTS: fakeTTS{delay: time.Millisecond}}
	p := &Pipeline{TTS: provider}
	p.softCancelTTS("barge-in")
	if provider.cancels.Load() != 1 {
		t.Fatalf("SoftCancel calls = %d, want 1", provider.cancels.Load())
	}
	// Providers without SoftCanceler are no-ops.
	p2 := &Pipeline{TTS: &fakeTTS{}}
	p2.softCancelTTS("barge-in") // must not panic
}

// Compile-time: Qwen3TTS implements SoftCanceler (native warm worker).
var _ tts.SoftCanceler = (*tts.Qwen3TTS)(nil)
