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
	// ThinkFull runs a non-streaming turn. opts.ToolsEnabled is the sole
	// runtime gate for tool calls — callers (pipeline) pass the same flag
	// used for ThinkStream so text and voice paths cannot diverge.
	ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error)
	ClearHistory()
	History() []Turn
	LoadHistory(turns []Turn)
}

// Warmer is an optional Provider capability that preloads the backend (e.g.
// loads the model into memory) so the user's first turn avoids the cold-start
// cost. Implementations are best-effort and must not block on failures.
type Warmer interface {
	Warmup(ctx context.Context)
}
