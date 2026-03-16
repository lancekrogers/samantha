package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/samantha/internal/config"
)

// OllamaBrain implements Provider using the Ollama API.
type OllamaBrain struct {
	client  *api.Client
	model   string
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

	return &OllamaBrain{
		client: client,
		model:  cfg.OllamaModel,
		cfg:    cfg,
	}, nil
}

// ThinkStream sends input and returns a channel of streaming text chunks.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string) (<-chan string, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	messages := o.buildMessages()
	stream := true
	req := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   &stream,
	}

	out := make(chan string, 8)
	go func() {
		defer close(out)
		var fullResponse strings.Builder

		err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
			if resp.Message.Content != "" {
				fullResponse.WriteString(resp.Message.Content)
				out <- resp.Message.Content
			}
			return nil
		})

		if err != nil {
			out <- fmt.Sprintf("[error: %v]", err)
		}

		response := fullResponse.String()
		if response != "" {
			response = cleanForVoice(response)
			o.history = append(o.history, api.Message{Role: "assistant", Content: response})
			o.trimHistory()
		}
	}()

	return out, nil
}

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string) (string, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	messages := o.buildMessages()
	stream := false
	req := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   &stream,
	}

	var response string
	err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		response = resp.Message.Content
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ollama error: %w", err)
	}

	response = strings.TrimSpace(response)
	if response == "" {
		response = "Hmm, I lost my train of thought for a second. What were you saying?"
	}

	response = cleanForVoice(response)
	o.history = append(o.history, api.Message{Role: "assistant", Content: response})
	o.trimHistory()

	return response, nil
}

// ClearHistory wipes conversation history.
func (o *OllamaBrain) ClearHistory() {
	o.history = nil
}

func (o *OllamaBrain) buildMessages() []api.Message {
	msgs := []api.Message{
		{Role: "system", Content: GetSystemPrompt(o.cfg.AgentName)},
	}

	// Include recent history.
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
