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

// SessionKind values for SessionState.Kind, shared by implementers and
// consumers so a typo cannot silently fall into the wrong display branch.
const (
	SessionKindHarness = "harness"
	SessionKindChat    = "chat"
)

// SessionState is a point-in-time description of who owns the conversation
// and how big it has grown, for display (the /session command).
type SessionState struct {
	// Kind is SessionKindHarness (a CLI session owns the transcript) or
	// SessionKindChat (samantha owns messages in-process).
	Kind string
	// ID is the harness session id being resumed; empty when none is live.
	ID string
	// PromptTokens is the last known replayed-prompt size (harness) or the
	// estimated next-request size (chat). 0 when unknown.
	PromptTokens int
	// Turns is the local history length retained for persistence/UI.
	Turns int
}

// SessionReporter is an optional Provider capability exposing live session
// state for UI display.
type SessionReporter interface {
	SessionInfo() SessionState
}
