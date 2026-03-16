package brain

import "context"

// Provider is the interface for all brain backends (Claude, Ollama, etc.).
type Provider interface {
	ThinkStream(ctx context.Context, input string) (<-chan string, error)
	ThinkFull(ctx context.Context, input string) (string, error)
	ClearHistory()
}
