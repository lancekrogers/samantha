package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/internal/config"
)

// GrokBrain manages conversation with Grok via grok-go-sdk, driving the local
// grok CLI the same way the Claude provider drives the claude CLI.
type GrokBrain struct {
	client  *grok.GrokClient
	cfg     *config.Config
	history []Turn
}

// NewGrok creates a Grok brain provider backed by the grok CLI.
func NewGrok(cfg *config.Config) (*GrokBrain, error) {
	client, err := grok.NewClientFromPath()
	if err != nil {
		return nil, fmt.Errorf("grok CLI not found on PATH: %w", err)
	}

	return &GrokBrain{
		client: client,
		cfg:    cfg,
	}, nil
}

// Available returns true if the grok CLI can be located.
func (g *GrokBrain) Available() bool {
	_, err := grok.LocateBinary()
	return err == nil
}

// ThinkStream sends input to Grok and returns a channel of streaming text chunks.
// Only spoken "text" events are forwarded; "thought" (reasoning) events are
// dropped so Samantha never voices her chain of thought.
func (g *GrokBrain) ThinkStream(ctx context.Context, input string, _ StreamOptions) (*Stream, error) {
	g.history = append(g.history, Turn{Role: "user", Content: input})
	prompt := g.buildPrompt()

	events, errs := g.client.StreamPrompt(ctx, prompt, g.runOptions(grok.StreamingJSONOutput))

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		var fullResponse strings.Builder
		var streamErr error

		for ev := range events {
			switch ev.Type {
			case grok.EventText:
				if text := ev.Content(); text != "" {
					fullResponse.WriteString(text)
					out <- text
				}
			case grok.EventError:
				if streamErr == nil {
					streamErr = fmt.Errorf("grok stream: %s", strings.TrimSpace(ev.Content()))
				}
			}
		}

		// Drain errors.
		for err := range errs {
			if err != nil && streamErr == nil {
				streamErr = fmt.Errorf("grok stream: %w", err)
			}
		}

		if streamErr != nil {
			done <- StreamResult{Err: streamErr}
			return
		}

		response := fullResponse.String()
		if response != "" {
			g.history = append(g.history, Turn{Role: "samantha", Content: response})
			g.trimHistory()
		}
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// ThinkFull sends input and waits for the complete response.
func (g *GrokBrain) ThinkFull(ctx context.Context, input string) (string, error) {
	g.history = append(g.history, Turn{Role: "user", Content: input})
	prompt := g.buildPrompt()

	result, err := g.client.RunPromptCtx(ctx, prompt, g.runOptions(grok.PlainOutput))
	if err != nil {
		return "", fmt.Errorf("grok error: %w", err)
	}

	response := strings.TrimSpace(result.Text)
	if response == "" {
		response = "Hmm, I lost my train of thought for a second. What were you saying?"
	}

	// Clean formatting
	response = cleanForVoice(response)

	g.history = append(g.history, Turn{Role: "samantha", Content: response})
	g.trimHistory()

	return response, nil
}

// runOptions builds the grok run options shared by the streaming and blocking
// paths. bypassPermissions mirrors the Claude provider so Samantha can use tools
// without repeated prompts; the grok SDK gates that mode behind AllowDangerousMode.
func (g *GrokBrain) runOptions(format grok.OutputFormat) *grok.RunOptions {
	opts := &grok.RunOptions{
		Format:               format,
		SystemPromptOverride: GetSystemPrompt(g.cfg.AgentName),
		PermissionMode:       grok.PermissionBypassPermissions,
		AllowDangerousMode:   true,
	}
	if g.cfg.GrokModel != "" {
		opts.Model = g.cfg.GrokModel
	}
	return opts
}

func (g *GrokBrain) buildPrompt() string {
	var parts []string

	// Include recent history for context
	recent := g.history
	if len(recent) > 6 {
		recent = recent[len(recent)-6:]
	}

	if len(recent) > 1 {
		parts = append(parts, "Recent conversation:")
		for _, t := range recent[:len(recent)-1] {
			speaker := "User"
			if t.Role == "samantha" {
				speaker = "Samantha"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", speaker, t.Content))
		}
		parts = append(parts, "")
	}

	parts = append(parts, fmt.Sprintf("User: %s", g.history[len(g.history)-1].Content))
	parts = append(parts, "")
	parts = append(parts, "Respond as Samantha. 2-3 sentences max, natural speech, NO markdown, NO formatting, NO code blocks, NO bullet points. Just talk naturally.")

	return strings.Join(parts, "\n")
}

func (g *GrokBrain) trimHistory() {
	max := g.cfg.MaxHistory * 2
	if len(g.history) > max {
		g.history = g.history[len(g.history)-max:]
	}
}

// History returns the conversation history.
func (g *GrokBrain) History() []Turn {
	return g.history
}

// ClearHistory wipes conversation history.
func (g *GrokBrain) ClearHistory() {
	g.history = nil
}

// LoadHistory restores conversation history from a saved session.
func (g *GrokBrain) LoadHistory(turns []Turn) {
	g.history = normalizePromptHistory(turns)
}
