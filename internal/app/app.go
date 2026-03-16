package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/samantha/internal/events"
	"github.com/Obedience-Corp/samantha/internal/pipeline"
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

// Run starts the main conversation loop.
func Run(ctx context.Context, p *pipeline.Pipeline, textMode, noVoice bool) error {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		var text string
		var err error

		if textMode {
			fmt.Print("  You: ")
			if !scanner.Scan() {
				return nil
			}
			text = strings.TrimSpace(scanner.Text())
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
