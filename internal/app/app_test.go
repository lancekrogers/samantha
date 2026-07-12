package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/stt"
)

func TestClassifyVoiceFailure(t *testing.T) {
	transient := errors.New("STT: stream reset failed")

	tests := []struct {
		name                string
		err                 error
		ctxErr              error
		consecutiveFailures int
		want                VoiceFailureAction
	}{
		{"context canceled error stops", context.Canceled, nil, 1, VoiceShutdown},
		{"wrapped context canceled stops", fmt.Errorf("brain: %w", context.Canceled), nil, 1, VoiceShutdown},
		{"cancelled context wins over transient error", transient, context.Canceled, 1, VoiceShutdown},
		{"first transient failure retries", transient, nil, 1, VoiceRetry},
		{"second transient failure retries", transient, nil, 2, VoiceRetry},
		{"sustained failures fall back", transient, nil, maxVoiceFailures, VoiceFallback},
		{"beyond threshold falls back", transient, nil, maxVoiceFailures + 1, VoiceFallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyVoiceFailure(tt.err, tt.ctxErr, tt.consecutiveFailures); got != tt.want {
				t.Fatalf("ClassifyVoiceFailure(%v, %v, %d) = %d, want %d",
					tt.err, tt.ctxErr, tt.consecutiveFailures, got, tt.want)
			}
		})
	}
}

func TestNormalizeCommand(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"Goodbye.", "goodbye"},
		{"GOODBYE!", "goodbye"},
		{"  exit  ", "exit"},
		{"talk later...", "talk later"},
		{"Reset?", "reset"},
		{"what's up", "what's up"}, // internal punctuation kept
		{"", ""},
	} {
		if got := NormalizeCommand(tt.in); got != tt.want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestVoiceCommandMatching(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		exit  bool
		clear bool
	}{
		// Sentences that merely mention a phrase must not trigger anything.
		{"router question does not clear", "Can you help me reset my router password?", false, false},
		{"mention of goodbye does not exit", "How do you say goodbye in French?", false, false},
		{"regular speech passes through", "Tell me about summer weather", false, false},
		{"punctuated goodbye exits", "Goodbye.", true, false},
		{"shouted goodbye exits", "GOODBYE!", true, false},
		{"multi-word exit phrase", "Talk later.", true, false},
		{"exact reset clears", "reset", false, true},
		{"punctuated reset clears", "Reset.", false, true},
		{"slash clear clears", "/clear", false, true},
		{"fresh start clears", "fresh start", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NormalizeCommand(tt.in)
			if got := IsExitCommand(cmd); got != tt.exit {
				t.Errorf("exitPhrases[NormalizeCommand(%q)] = %v, want %v", tt.in, got, tt.exit)
			}
			if got := IsClearCommand(cmd); got != tt.clear {
				t.Errorf("IsClearCommand(NormalizeCommand(%q)) = %v, want %v", tt.in, got, tt.clear)
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
		if got := IsResumeVoiceCommand(tt.cmd); got != tt.want {
			t.Errorf("IsResumeVoiceCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// TestLineReaderNextCancels: next unblocks when ctx is cancelled mid-wait.
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

// TestRunReturnsOnCancel: Run unwinds when ctx is cancelled while awaiting input.
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

// failingSTT always fails to start a session, so every voice turn errors.
type failingSTT struct{}

func (failingSTT) Start(context.Context) (stt.Session, error) {
	return nil, errors.New("stt unavailable")
}

func (failingSTT) Available() bool { return true }

// TestRunFallsBackToTextAfterSustainedVoiceFailures: with an always-failing STT,
// Run retries then falls back to text input, where the queued "exit" ends it.
func TestRunFallsBackToTextAfterSustainedVoiceFailures(t *testing.T) {
	p := &pipeline.Pipeline{Events: events.NewBus(), STT: failingSTT{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p, strings.NewReader("exit\n"), false /* textMode */, false)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never fell back to text input after sustained voice failures")
	}
}
