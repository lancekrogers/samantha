package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

// scriptedStream builds a brain.Stream that emits the given chunks then reports
// doneErr on its terminal channel, mirroring a real provider stream without a
// model. It honors ctx so cancellation tests do not leak the writer goroutine.
func scriptedStream(ctx context.Context, chunks []string, doneErr error) *brain.Stream {
	out := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case out <- c:
			}
		}
		done <- brain.StreamResult{Err: doneErr}
	}()
	return &brain.Stream{Chunks: out, Done: done}
}

func collect(t *testing.T, ch <-chan string) []string {
	t.Helper()
	var got []string
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, v)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out collecting from stage channel")
		}
	}
}

func TestObserveStreamForwardsChunksAndMarksFirstChunk(t *testing.T) {
	bus := events.NewBus()
	var started events.ResponseStreamingStarted
	sawStart := 0
	events.Subscribe(bus, func(e events.ResponseStreamingStarted) {
		started = e
		sawStart++
	})

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"Hello ", "there. ", "How are you?"}, nil)

	chunks, _ := p.observeStream(context.Background(), stream, metrics)
	got := collect(t, chunks)

	want := []string{"Hello ", "there. ", "How are you?"}
	if len(got) != len(want) {
		t.Fatalf("observeStream forwarded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
	if sawStart != 1 {
		t.Fatalf("ResponseStreamingStarted emitted %d times, want exactly 1 (first chunk only)", sawStart)
	}
	if started.Elapsed < 0 {
		t.Fatalf("ResponseStreamingStarted.Elapsed = %v, want >= 0", started.Elapsed)
	}
	if metrics.firstModelChunk.IsZero() {
		t.Fatal("metrics.firstModelChunk was not stamped on the first chunk")
	}
}

func TestObserveStreamEmitsResponseDeltaPerChunk(t *testing.T) {
	bus := events.NewBus()
	var deltas []string
	events.Subscribe(bus, func(e events.ResponseDelta) {
		deltas = append(deltas, e.Text)
	})

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"Hel", "lo ", "world."}, nil)

	chunks, _ := p.observeStream(context.Background(), stream, metrics)
	_ = collect(t, chunks) // drain so the observer runs to completion

	want := []string{"Hel", "lo ", "world."}
	if len(deltas) != len(want) {
		t.Fatalf("ResponseDelta events = %v, want %v", deltas, want)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Fatalf("delta %d = %q, want %q", i, deltas[i], want[i])
		}
	}
}

// TestRunTurnTextModeStreamsDeltas pins that typed input streams token-by-token:
// each brain chunk becomes a ResponseDelta and ResponseReady carries the full,
// trimmed reply. No TTS/Player is wired, so the voice branch is skipped.
func TestRunTurnTextModeStreamsDeltas(t *testing.T) {
	bus := events.NewBus()
	var deltas []string
	events.Subscribe(bus, func(e events.ResponseDelta) { deltas = append(deltas, e.Text) })
	var final events.ResponseReady
	events.Subscribe(bus, func(e events.ResponseReady) { final = e })

	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"Hello ", "world."}}, Events: bus}

	if err := p.RunTurnTextMode(context.Background(), "hi"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}

	want := []string{"Hello ", "world."}
	if len(deltas) != len(want) {
		t.Fatalf("ResponseDelta events = %v, want %v", deltas, want)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Fatalf("delta %d = %q, want %q", i, deltas[i], want[i])
		}
	}
	if final.Response != "Hello world." {
		t.Fatalf("ResponseReady.Response = %q, want %q", final.Response, "Hello world.")
	}
}

func TestObserveStreamClosesOnStreamCompletion(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"done."}, nil)

	out, _ := p.observeStream(context.Background(), stream, metrics)
	_ = collect(t, out) // drain to completion

	// A second receive on a drained, closed channel must not block.
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("observeStream channel produced a value after completion")
		}
	case <-time.After(time.Second):
		t.Fatal("observeStream channel did not close on stream completion")
	}
}

func TestObserveStreamModelErrorClosesObserver(t *testing.T) {
	// A model error is delivered on stream.Done; the observer only watches
	// Chunks, which still close, so the stage terminates cleanly either way.
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"partial"}, context.DeadlineExceeded)

	chunks, _ := p.observeStream(context.Background(), stream, metrics)
	got := collect(t, chunks)
	if len(got) != 1 || got[0] != "partial" {
		t.Fatalf("observeStream forwarded %v, want [partial] before the model error", got)
	}
}

func TestObserveStreamCancellationClosesPromptly(t *testing.T) {
	// No downstream reader: with the cancellation-safe send, ctx cancel must
	// still close the observer channel rather than wedge on a full buffer.
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()

	ctx, cancel := context.WithCancel(context.Background())
	manyChunks := make([]string, 64)
	for i := range manyChunks {
		manyChunks[i] = "x"
	}
	out, _ := p.observeStream(ctx, scriptedStream(ctx, manyChunks, nil), metrics)

	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return // closed: goroutine exited, no leak
			}
		case <-deadline:
			t.Fatal("observeStream did not close promptly after cancellation")
		}
	}
}

func TestSegmentStageProducesSentencesWithoutPlayback(t *testing.T) {
	// The full upstream pipeline (observe -> segment) runs with no TTS/Player.
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"First sentence. Second one! ", "Third?"}, nil)

	chunks, _ := p.observeStream(context.Background(), stream, metrics)
	sentences := collect(t, brain.ChunkSentences(chunks))

	want := []string{"First sentence.", "Second one!", "Third?"}
	if len(sentences) != len(want) {
		t.Fatalf("segment stage produced %v, want %v", sentences, want)
	}
	for i := range want {
		if sentences[i] != want[i] {
			t.Fatalf("sentence %d = %q, want %q", i, sentences[i], want[i])
		}
	}
}

// TestStreamResponseNoRaceOnFirstChunkCancel guards the fix for the data race
// between observeStream stamping metrics.firstModelChunk and finish()/snapshot()
// reading it: streamResponse must join the observer before returning, so a
// cancellation landing exactly as the first chunk arrives cannot leave the
// goroutine writing after the caller reads. Run under -race to detect a
// regression.
func TestStreamResponseNoRaceOnFirstChunkCancel(t *testing.T) {
	for range 200 {
		p := &Pipeline{Events: events.NewBus()}
		metrics := newTurnMetrics()
		turn := p.newTurnConductor(metrics)
		ctx, cancel := context.WithCancel(context.Background())

		stream := scriptedStream(ctx, []string{"hello", "there"}, nil)
		go cancel() // cancel concurrently with the first chunk
		_, _, _ = p.streamResponse(ctx, cancel, stream, false, metrics, turn)
		_ = metrics.snapshot() // reads firstModelChunk after streamResponse returned
	}
}

func TestSegmentStageFlushesTrailingPartialSentence(t *testing.T) {
	// A sentence split across chunks with no terminal punctuation must still be
	// flushed as the final segment.
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	stream := scriptedStream(context.Background(), []string{"a trailing ", "thought with no period"}, nil)

	chunks, _ := p.observeStream(context.Background(), stream, metrics)
	sentences := collect(t, brain.ChunkSentences(chunks))

	if len(sentences) != 1 || sentences[0] != "a trailing thought with no period" {
		t.Fatalf("segment stage flush = %v, want [a trailing thought with no period]", sentences)
	}
}
