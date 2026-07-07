package brain

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/config"
)

// BatchRequest describes a single-shot, history-free text transformation.
// The caller supplies the full system prompt; no persona is injected and no
// interactive session history is read or written.
type BatchRequest struct {
	SystemPrompt  string
	StylePrompt   string
	Pronunciation string
	SectionTitle  string
	Text          string
	Metadata      map[string]string
}

// BatchResult carries the transformed text and the identity of the provider
// and model that produced it.
type BatchResult struct {
	Text     string
	Provider string
	Model    string
}

// BatchProvider transforms text in a single self-contained call.
type BatchProvider interface {
	Transform(ctx context.Context, req BatchRequest) (BatchResult, error)
}

// AssembleBatchPrompt renders req into its canonical prompt string: system
// prompt, style prompt, pronunciation guide, section title, metadata (sorted
// by key), then the text, joined by blank lines with empty parts skipped.
// Downstream caching hashes this string, so the order and formatting must
// stay stable. Adapters send the same content, with the system prompt routed
// through the provider's native system slot and the rest as the user prompt.
func AssembleBatchPrompt(req BatchRequest) string {
	return joinPromptParts(req.SystemPrompt, batchPromptBody(req))
}

// batchPromptBody assembles every part after the system prompt — the portion
// adapters send as the user prompt.
func batchPromptBody(req BatchRequest) string {
	parts := []string{req.StylePrompt, req.Pronunciation}
	if req.SectionTitle != "" {
		parts = append(parts, "Section: "+req.SectionTitle)
	}
	if len(req.Metadata) > 0 {
		keys := make([]string, 0, len(req.Metadata))
		for k := range req.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k + ": " + req.Metadata[k]
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	parts = append(parts, req.Text)
	return joinPromptParts(parts...)
}

func joinPromptParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// ollamaBatch adapts the Ollama chat client to BatchProvider.
type ollamaBatch struct {
	client *api.Client
	model  string
}

// newOllamaBatch reuses the interactive constructor for its connection and
// model checks, then keeps only the client — batch calls never touch the
// interactive brain or its history.
func newOllamaBatch(cfg *config.Config) (*ollamaBatch, error) {
	b, err := NewOllama(cfg)
	if err != nil {
		return nil, err
	}
	return &ollamaBatch{client: b.client, model: b.model}, nil
}

func (o *ollamaBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	var messages []api.Message
	if req.SystemPrompt != "" {
		messages = append(messages, api.Message{Role: "system", Content: req.SystemPrompt})
	}
	messages = append(messages, api.Message{Role: "user", Content: batchPromptBody(req)})

	stream := false
	chatReq := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   &stream,
	}

	var response api.Message
	err := o.client.Chat(ctx, chatReq, func(resp api.ChatResponse) error {
		response = resp.Message
		return nil
	})
	if err != nil {
		return BatchResult{}, fmt.Errorf("ollama batch: %w", err)
	}

	text := strings.TrimSpace(response.Content)
	if text == "" {
		return BatchResult{}, fmt.Errorf("ollama batch: empty response from model %s", o.model)
	}
	return BatchResult{Text: text, Provider: "ollama", Model: o.model}, nil
}

// grokPromptRunner is the slice of the grok client the batch adapter needs.
type grokPromptRunner interface {
	RunPromptCtx(ctx context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error)
}

// grokBatch adapts the grok CLI client to BatchProvider.
type grokBatch struct {
	runner grokPromptRunner
	model  string
}

func newGrokBatch(cfg *config.Config) (*grokBatch, error) {
	g, err := NewGrok(cfg)
	if err != nil {
		return nil, err
	}
	return &grokBatch{runner: g.client, model: cfg.GrokModel}, nil
}

func (g *grokBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	opts := &grok.RunOptions{
		Format:               grok.PlainOutput,
		SystemPromptOverride: req.SystemPrompt,
		Model:                g.model,
	}

	result, err := g.runner.RunPromptCtx(ctx, batchPromptBody(req), opts)
	if err != nil {
		return BatchResult{}, fmt.Errorf("grok batch: %w", err)
	}

	text := strings.TrimSpace(result.Text)
	if text == "" {
		return BatchResult{}, fmt.Errorf("grok batch: empty response from model %s", g.modelID())
	}
	return BatchResult{Text: text, Provider: "grok", Model: g.modelID()}, nil
}

// modelID reports the model identity even when the config leaves selection to
// the grok CLI.
func (g *grokBatch) modelID() string {
	if g.model == "" {
		return "default"
	}
	return g.model
}
