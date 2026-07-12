package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

const (
	maxVoiceFailures = 3
	// RetryBackoff is the pause between voice-turn retries after a transient
	// failure.
	RetryBackoff = 500 * time.Millisecond
)

// VoiceFailureAction is the policy decision after a failed voice turn.
type VoiceFailureAction int

const (
	VoiceRetry VoiceFailureAction = iota
	VoiceFallback
	VoiceShutdown
)

// ClassifyVoiceFailure decides whether a failed voice turn should retry,
// fall back to text input, or shut the loop down.
func ClassifyVoiceFailure(err, ctxErr error, consecutiveFailures int) VoiceFailureAction {
	if errors.Is(err, context.Canceled) || ctxErr != nil {
		return VoiceShutdown
	}
	if consecutiveFailures >= maxVoiceFailures {
		return VoiceFallback
	}
	return VoiceRetry
}

// IsResumeVoiceCommand matches the command that re-enables voice turns after
// a fallback to text.
func IsResumeVoiceCommand(cmd string) bool {
	return cmd == "/voice" || cmd == "/v"
}

// IsExitCommand matches a normalized transcript against the exit phrases.
func IsExitCommand(cmd string) bool {
	return exitPhrases[cmd]
}

// NormalizeCommand prepares a transcript for command matching: Whisper output
// carries casing and trailing punctuation ("Goodbye.") that exact matches miss.
func NormalizeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimRight(s, ".,!?"))
}

// IsClearCommand matches exactly — substring matching wiped history on
// sentences that merely mentioned "reset".
func IsClearCommand(cmd string) bool {
	return cmd == "/clear" || cmd == "/c" || slices.Contains(clearPhrases, cmd)
}

var exitPhrases = map[string]bool{
	"exit": true, "quit": true, "bye": true, "goodbye": true, "stop": true,
	"/exit": true, "/q": true, "gotta go": true, "got to go": true,
	"i'm out": true, "i'm done": true, "wrap up": true, "talk later": true,
	"see you later": true, "see ya": true, "good night": true,
	"signing off": true, "peace out": true, "catch you later": true,
	"bye samantha": true, "bye bye": true, "that's all": true,
	"we're done": true, "samantha exit": true, "samantha quit": true,
	"samantha bye": true,
}

var clearPhrases = []string{
	"forget everything", "start over", "clear the conversation",
	"fresh start", "new conversation", "reset",
}

// Run drives the conversation loop until the user exits or ctx is cancelled.
// Text input is read from in cancellably so a stdin read never blocks shutdown.
func Run(ctx context.Context, p *pipeline.Pipeline, in io.Reader, textMode, noVoice bool) error {
	var input *lineReader // started lazily so voice mode never touches stdin
	voiceAvailable := p.STT != nil
	voiceFailures := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		var text string

		if textMode {
			if input == nil {
				input = newLineReader(ctx, in)
			}
			fmt.Print("  You: ")
			line, ok := input.next(ctx)
			if !ok {
				return nil
			}
			text = strings.TrimSpace(line)
			if text == "" {
				continue
			}
		} else {
			turnText, err := p.RunTurn(ctx)
			if err != nil {
				switch ClassifyVoiceFailure(err, ctx.Err(), voiceFailures+1) {
				case VoiceShutdown:
					return nil
				case VoiceFallback:
					p.Events.Emit(events.Error{Message: err.Error()})
					p.Events.Emit(events.Info{Message: "Voice input keeps failing — switching to text. Type /voice to switch back."})
					textMode = true
					voiceFailures = 0
					continue
				case VoiceRetry:
					voiceFailures++
					p.Events.Emit(events.Error{Message: err.Error()})
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(RetryBackoff):
					}
					continue
				}
			}
			voiceFailures = 0
			if turnText == "" {
				continue // silence, keep listening
			}
			text = turnText
		}

		cmd := NormalizeCommand(text)

		// Exit check
		if exitPhrases[cmd] {
			return nil
		}

		// Resume voice after a fallback.
		if textMode && voiceAvailable && IsResumeVoiceCommand(cmd) {
			textMode = false
			voiceFailures = 0
			p.Events.Emit(events.Info{Message: "Switching back to voice mode."})
			continue
		}

		// Clear check
		if IsClearCommand(cmd) {
			p.Brain.ClearHistory()
			p.Events.Emit(events.ConversationCleared{})
			continue
		}

		// In text mode, we need to run the turn manually
		if textMode {
			if err := p.RunTurnTextMode(ctx, text); err != nil {
				p.Events.Emit(events.Error{Message: err.Error()})
			}
		}
	}
}

// lineReader reads lines in a background goroutine so the loop can wait on input
// and ctx cancellation at once; a blocking stdin read can't be interrupted in
// place.
type lineReader struct {
	lines chan string
}

func newLineReader(ctx context.Context, r io.Reader) *lineReader {
	lr := &lineReader{lines: make(chan string, 1)}
	go func() {
		defer close(lr.lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			select {
			case lr.lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return lr
}

// next returns the next line, or ok=false if input is exhausted or ctx is
// cancelled.
func (lr *lineReader) next(ctx context.Context) (string, bool) {
	select {
	case <-ctx.Done():
		return "", false
	case line, ok := <-lr.lines:
		return line, ok
	}
}
