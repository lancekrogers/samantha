package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/samantha/internal/config"
)

// OllamaBrain implements Provider using the Ollama API with tool calling.
type OllamaBrain struct {
	client  *api.Client
	model   string
	workDir string
	history []api.Message
	cfg     *config.Config
}

// NewOllama creates an Ollama brain provider.
func NewOllama(cfg *config.Config) (*OllamaBrain, error) {
	if cfg.OllamaModel == "" {
		return nil, fmt.Errorf("ollama_model not configured — run: samantha config ollama_model <model>")
	}

	base, err := url.Parse(cfg.OllamaHost)
	if err != nil {
		return nil, fmt.Errorf("invalid ollama_host %q: %w", cfg.OllamaHost, err)
	}

	client := api.NewClient(base, http.DefaultClient)

	// Verify model exists.
	models, err := client.List(context.Background())
	if err != nil {
		return nil, fmt.Errorf("cannot connect to ollama at %s: %w", cfg.OllamaHost, err)
	}

	found := false
	for _, m := range models.Models {
		if m.Name == cfg.OllamaModel || strings.TrimSuffix(m.Name, ":latest") == cfg.OllamaModel {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("model %q not found in ollama — run: ollama pull %s", cfg.OllamaModel, cfg.OllamaModel)
	}

	workDir, _ := os.Getwd()

	return &OllamaBrain{
		client:  client,
		model:   cfg.OllamaModel,
		workDir: workDir,
		cfg:     cfg,
	}, nil
}

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string) (<-chan string, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	out := make(chan string, 8)
	go func() {
		defer close(out)

		tools := voiceAssistantTools()

		for i := 0; i < maxToolIterations; i++ {
			messages := o.buildMessages()
			stream := true
			req := &api.ChatRequest{
				Model:    o.model,
				Messages: messages,
				Tools:    tools,
				Stream:   &stream,
			}

			// Accumulate the full response (text + tool calls).
			var textBuf strings.Builder
			var toolCalls []api.ToolCall

			err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
				if resp.Message.Content != "" {
					textBuf.WriteString(resp.Message.Content)
				}
				if len(resp.Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, resp.Message.ToolCalls...)
				}
				return nil
			})
			if err != nil {
				out <- fmt.Sprintf("[error: %v]", err)
				return
			}

			// If model made tool calls, execute them and loop.
			if len(toolCalls) > 0 {
				// Add the assistant's tool-calling message to history.
				o.history = append(o.history, api.Message{
					Role:      "assistant",
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				})

				// Execute each tool and add results.
				for _, tc := range toolCalls {
					result := executeTool(ctx, o.workDir, tc)
					o.history = append(o.history, api.Message{
						Role:    "tool",
						Content: result,
					})
				}
				continue // re-request with tool results
			}

			// No tool calls — stream text to output.
			response := textBuf.String()
			if response != "" {
				out <- response
				response = cleanForVoice(response)
				o.history = append(o.history, api.Message{Role: "assistant", Content: response})
				o.trimHistory()
			}
			return
		}

		out <- "I seem to be going in circles with my tools. Let me just answer directly."
	}()

	return out, nil
}

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string) (string, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	tools := voiceAssistantTools()

	for i := 0; i < maxToolIterations; i++ {
		messages := o.buildMessages()
		stream := false
		req := &api.ChatRequest{
			Model:    o.model,
			Messages: messages,
			Tools:    tools,
			Stream:   &stream,
		}

		var response api.Message
		err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
			response = resp.Message
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("ollama error: %w", err)
		}

		// Tool calls — execute and loop.
		if len(response.ToolCalls) > 0 {
			o.history = append(o.history, api.Message{
				Role:      "assistant",
				Content:   response.Content,
				ToolCalls: response.ToolCalls,
			})

			for _, tc := range response.ToolCalls {
				result := executeTool(ctx, o.workDir, tc)
				o.history = append(o.history, api.Message{
					Role:    "tool",
					Content: result,
				})
			}
			continue
		}

		// Text response.
		text := strings.TrimSpace(response.Content)
		if text == "" {
			text = "Hmm, I lost my train of thought for a second. What were you saying?"
		}
		text = cleanForVoice(text)
		o.history = append(o.history, api.Message{Role: "assistant", Content: text})
		o.trimHistory()
		return text, nil
	}

	return "I seem to be going in circles with my tools. Let me just answer directly.", nil
}

// ClearHistory wipes conversation history.
func (o *OllamaBrain) ClearHistory() {
	o.history = nil
}

// History returns conversation history as Turn slices for session persistence.
func (o *OllamaBrain) History() []Turn {
	turns := make([]Turn, len(o.history))
	for i, m := range o.history {
		turns[i] = Turn{Role: m.Role, Content: m.Content}
	}
	return turns
}

// LoadHistory restores conversation history from a saved session.
func (o *OllamaBrain) LoadHistory(turns []Turn) {
	o.history = make([]api.Message, len(turns))
	for i, t := range turns {
		o.history[i] = api.Message{Role: t.Role, Content: t.Content}
	}
}

func (o *OllamaBrain) buildMessages() []api.Message {
	systemPrompt := GetSystemPrompt(o.cfg.AgentName) + "\n" + EnvironmentContext(o.workDir)

	msgs := []api.Message{
		{Role: "system", Content: systemPrompt},
	}

	recent := o.history
	max := o.cfg.MaxHistory * 2
	if len(recent) > max {
		recent = recent[len(recent)-max:]
	}

	msgs = append(msgs, recent...)
	return msgs
}

func (o *OllamaBrain) trimHistory() {
	max := o.cfg.MaxHistory * 2
	if len(o.history) > max {
		o.history = o.history[len(o.history)-max:]
	}
}
