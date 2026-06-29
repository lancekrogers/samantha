package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

// TestLineReaderNextCancels verifies that next unblocks promptly when the
// context is cancelled while waiting on input that never arrives — the core of
// the fix for the unkillable-on-SIGTERM hang in text mode.
func TestLineReaderNextCancels(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close() // unblock the reader goroutine when the test ends

	ctx, cancel := context.WithCancel(context.Background())
	lr := newLineReader(ctx, pr)

	type result struct {
		line string
		ok   bool
	}
	resCh := make(chan result, 1)
	go func() {
		line, ok := lr.next(ctx)
		resCh <- result{line, ok}
	}()

	cancel()

	select {
	case r := <-resCh:
		if r.ok {
			t.Fatalf("expected ok=false after cancellation, got line=%q ok=%v", r.line, r.ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lineReader.next did not return within 2s after context cancellation")
	}
}

// TestRunReturnsOnCancel verifies that the main loop unwinds when ctx is
// cancelled while blocked waiting for text input, instead of hanging on a
// blocking stdin read.
func TestRunReturnsOnCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	p := &pipeline.Pipeline{Events: events.NewBus()}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p, pr, true /* textMode */, false)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancellation")
	}
}
