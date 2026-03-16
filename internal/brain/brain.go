package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/samantha/internal/config"
)

// Brain manages conversation with Claude via claude-code-go.
type Brain struct {
	client  *claude.ClaudeClient
	cfg     *config.Config
	history []Turn
}

// Turn represents a single conversation exchange.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// New creates a Brain instance.
func New(cfg *config.Config) (*Brain, error) {
	binPath, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude CLI not found on PATH: %w", err)
	}

	client := claude.NewClient(binPath)

	return &Brain{
		client: client,
		cfg:    cfg,
	}, nil
}

// Available returns true if the claude CLI is on PATH.
func (b *Brain) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// ThinkStream sends input to Claude and returns a channel of streaming message chunks.
// Each message on the channel may contain partial text.
func (b *Brain) ThinkStream(ctx context.Context, input string) (<-chan string, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})
	prompt := b.buildPrompt()

	opts := &claude.RunOptions{
		Format:                 claude.StreamJSONOutput,
		SystemPrompt:           GetSystemPrompt(),
		Model:                  b.cfg.ClaudeModel,
		PermissionMode:         claude.PermissionModeBypassPermissions,
		IncludePartialMessages: true,
	}

	messages, errs := b.client.StreamPrompt(ctx, prompt, opts)

	out := make(chan string, 8)
	go func() {
		defer close(out)
		var fullResponse strings.Builder

		for msg := range messages {
			// Extract text content from assistant messages
			if msg.Type == "assistant" {
				var content struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(msg.Message, &content); err == nil {
					for _, c := range content.Content {
						if c.Type == "text" && c.Text != "" {
							fullResponse.WriteString(c.Text)
							out <- c.Text
						}
					}
				}
			}

			// Check for final result
			if msg.Type == "result" && msg.Result != "" {
				if fullResponse.Len() == 0 {
					fullResponse.WriteString(msg.Result)
					out <- msg.Result
				}
			}
		}

		// Drain errors
		for err := range errs {
			if err != nil {
				out <- fmt.Sprintf("[error: %v]", err)
			}
		}

		response := fullResponse.String()
		if response != "" {
			b.history = append(b.history, Turn{Role: "samantha", Content: response})
			b.trimHistory()
		}
	}()

	return out, nil
}

// ThinkFull sends input and waits for the complete response.
func (b *Brain) ThinkFull(ctx context.Context, input string) (string, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})
	prompt := b.buildPrompt()

	opts := &claude.RunOptions{
		Format:         claude.TextOutput,
		SystemPrompt:   GetSystemPrompt(),
		Model:          b.cfg.ClaudeModel,
		PermissionMode: claude.PermissionModeBypassPermissions,
	}

	result, err := b.client.RunPromptCtx(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("claude error: %w", err)
	}

	response := strings.TrimSpace(result.Result)
	if response == "" {
		response = "Hmm, I lost my train of thought for a second. What were you saying?"
	}

	// Clean formatting
	response = cleanForVoice(response)

	b.history = append(b.history, Turn{Role: "samantha", Content: response})
	b.trimHistory()

	return response, nil
}

func (b *Brain) buildPrompt() string {
	var parts []string

	// Include recent history for context
	recent := b.history
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

	parts = append(parts, fmt.Sprintf("User: %s", b.history[len(b.history)-1].Content))
	parts = append(parts, "")
	parts = append(parts, "Respond as Samantha. 2-3 sentences max, natural speech, NO markdown, NO formatting, NO code blocks, NO bullet points. Just talk naturally.")

	return strings.Join(parts, "\n")
}

func (b *Brain) trimHistory() {
	max := b.cfg.MaxHistory * 2
	if len(b.history) > max {
		b.history = b.history[len(b.history)-max:]
	}
}

// History returns the conversation history.
func (b *Brain) History() []Turn {
	return b.history
}

// ClearHistory wipes conversation history.
func (b *Brain) ClearHistory() {
	b.history = nil
}

func cleanForVoice(s string) string {
	r := strings.NewReplacer("**", "", "```", "", "##", "", "# ", "")
	return strings.TrimSpace(r.Replace(s))
}
