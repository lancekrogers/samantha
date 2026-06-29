package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

// TestClassifyVoiceFailure covers the recovery policy: a transient voice-turn
// failure is retried in voice mode, voice is abandoned only after sustained
// failures, and context cancellation always stops the loop.
func TestClassifyVoiceFailure(t *testing.T) {
	transient := errors.New("STT: stream reset failed")

	tests := []struct {
		name                string
		err                 error
		ctxErr              error
		consecutiveFailures int
		want                voiceFailureAction
	}{
		{"context canceled error stops", context.Canceled, nil, 1, voiceShutdown},
		{"wrapped context canceled stops", fmt.Errorf("brain: %w", context.Canceled), nil, 1, voiceShutdown},
		{"cancelled context wins over transient error", transient, context.Canceled, 1, voiceShutdown},
		{"first transient failure retries", transient, nil, 1, voiceRetry},
		{"second transient failure retries", transient, nil, 2, voiceRetry},
		{"sustained failures fall back", transient, nil, maxVoiceFailures, voiceFallback},
		{"beyond threshold falls back", transient, nil, maxVoiceFailures + 1, voiceFallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyVoiceFailure(tt.err, tt.ctxErr, tt.consecutiveFailures); got != tt.want {
				t.Fatalf("classifyVoiceFailure(%v, %v, %d) = %d, want %d",
					tt.err, tt.ctxErr, tt.consecutiveFailures, got, tt.want)
			}
		})
	}
}

func TestIsResumeVoiceCommand(t *testing.T) {
	for _, tt := range []struct {
		cmd  string
		want bool
	}{
		{"/voice", true},
		{"/v", true},
		{"voice", false},
		{"/voices", false},
		{"hello", false},
		{"", false},
	} {
		if got := isResumeVoiceCommand(tt.cmd); got != tt.want {
			t.Errorf("isResumeVoiceCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

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
