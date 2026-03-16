package brain

import "context"

// StreamOptions controls how a streamed brain response should behave.
type StreamOptions struct {
	VoiceMode    bool
	ToolsEnabled bool
}

// Provider is the interface for all brain backends (Claude, Ollama, etc.).
type Provider interface {
	ThinkStream(ctx context.Context, input string, opts StreamOptions) (<-chan string, error)
	ThinkFull(ctx context.Context, input string) (string, error)
	ClearHistory()
	History() []Turn
	LoadHistory(turns []Turn)
}
