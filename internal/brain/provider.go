package brain

import "context"

// StreamOptions controls how a streamed brain response should behave.
type StreamOptions struct {
	VoiceMode    bool
	ToolsEnabled bool
}

// StreamResult reports the terminal outcome of a streamed response.
type StreamResult struct {
	Err error
}

// Stream carries a streamed model response and its terminal result.
type Stream struct {
	Chunks <-chan string
	Done   <-chan StreamResult
}

// Provider is the interface for all brain backends (Claude, Ollama, etc.).
type Provider interface {
	ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error)
	ThinkFull(ctx context.Context, input string) (string, error)
	ClearHistory()
	History() []Turn
	LoadHistory(turns []Turn)
}
