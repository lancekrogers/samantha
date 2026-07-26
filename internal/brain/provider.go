package brain

import "context"

// StreamOptions controls how a streamed brain response should behave.
type StreamOptions struct {
	VoiceMode    bool
	ToolsEnabled bool
	// OnToolStart is invoked just before a tool runs (optional; for UI).
	OnToolStart func(name, summary string)
	// OnToolEnd is invoked after a tool returns (optional; for UI).
	OnToolEnd func(name, preview string)
	// OnUsage reports one model request's token accounting (optional; for
	// UI). prefill is the prompt tokens the server actually processed — with
	// a warm KV prefix cache it should track the size of the new turn, not
	// the whole transcript — and gen is the tokens generated.
	OnUsage func(prefill, gen int)
	// OnSessionWarn reports that the provider's replayed session prompt has
	// crossed the configured warn threshold (optional; for UI). Fired at most
	// once per underlying session; the session itself is left alone.
	OnSessionWarn func(promptTokens, threshold int)
}

// StreamResult reports the terminal outcome of a streamed response.
type StreamResult struct {
	Err error
	// Recovered marks that Err was already converted into a spoken recovery
	// reply streamed through Chunks (and recorded in history). Consumers must
	// treat the turn as degraded-complete and surface Err as detail only.
	Recovered bool
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
