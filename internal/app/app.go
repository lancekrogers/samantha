package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

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

// Run starts the main conversation loop. Text input is read from in (typically
// os.Stdin) via a cancellable reader, so the loop unwinds promptly when ctx is
// cancelled even while waiting for typed input.
func Run(ctx context.Context, p *pipeline.Pipeline, in io.Reader, textMode, noVoice bool) error {
	var input *lineReader // started lazily so voice mode never touches stdin

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		var text string
		var err error

		if textMode {
			if input == nil {
				input = newLineReader(ctx, in)
			}
			fmt.Print("  You: ")
			line, ok := input.next(ctx)
			if !ok {
				return nil // EOF or context cancelled
			}
			text = strings.TrimSpace(line)
			if text == "" {
				continue
			}
		} else {
			text, err = p.RunTurn(ctx)
			if err != nil {
				p.Events.Emit(events.Error{Message: err.Error()})
				p.Events.Emit(events.Info{Message: "Switching to text mode."})
				textMode = true
				continue
			}
			if text == "" {
				continue // silence, keep listening
			}
		}

		cmd := strings.ToLower(strings.TrimSpace(text))

		// Exit check
		if exitPhrases[cmd] {
			return nil
		}

		// Clear check
		isClear := cmd == "/clear" || cmd == "/c"
		if !isClear {
			for _, phrase := range clearPhrases {
				if strings.Contains(cmd, phrase) {
					isClear = true
					break
				}
			}
		}
		if isClear {
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
