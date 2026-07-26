package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
)

// claudeRunner is the slice of the claude client the interactive Brain needs:
// the streaming path for voice/TUI turns and the blocking path for text turns.
// Narrowing it to an interface lets tests inject a fake without shelling out to
// a real claude CLI. *claude.ClaudeClient satisfies it.
type claudeRunner interface {
	StreamPrompt(ctx context.Context, prompt string, opts *claude.RunOptions) (<-chan claude.Message, <-chan error)
	RunPromptCtx(ctx context.Context, prompt string, opts *claude.RunOptions) (*claude.ClaudeResult, error)
}

// Brain manages conversation with Claude via claude-code-go.
type Brain struct {
	client          claudeRunner
	cfg             *config.Config
	systemPrompt    string
	turnInstruction string
	history         []Turn
	// sessionID is the claude CLI session captured on the first turn and reused
	// via --resume thereafter, so subsequent turns send only the new input and
	// get Anthropic-side prompt caching. Empty until the first turn captures it;
	// scoped to this Brain, which is constructed per conversation.
	sessionID string
	// promptTokens is the prompt size the CLI reported for the most recent
	// request: new input plus whatever the cache served. Resuming replays the
	// whole transcript, so this is the number that grows without bound and the
	// one the session budget watches. ThinkFull falls back to a local estimate
	// because ClaudeResult drops usage.
	promptTokens int
}

// claudeUsage is the token accounting the CLI reports on each assistant
// message. Prompt tokens are split across three fields depending on what the
// cache served, so the real prompt size is their sum.
type claudeUsage struct {
	InputTokens         int `json:"input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	OutputTokens        int `json:"output_tokens"`
}

func (u claudeUsage) prompt() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
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

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}

	return &Brain{
		client:          client,
		cfg:             cfg,
		systemPrompt:    systemPrompt,
		turnInstruction: turn,
	}, nil
}

// Available returns true if the claude CLI is on PATH.
func (b *Brain) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// runOptions builds the per-turn claude run options. Tool execution is gated
// by voice_tools_enabled: disabled removes every built-in tool and uses the
// default permission mode, while enabled keeps the default tool set with
// bypassPermissions so hands-free tool calls don't stall on permission
// prompts.
func (b *Brain) runOptions(format claude.OutputFormat, toolsEnabled bool) *claude.RunOptions {
	mode := claude.PermissionModeDefault
	var tools []string
	if toolsEnabled {
		mode = claude.PermissionModeBypassPermissions
	} else {
		// The Claude CLI reserves an empty --tools value for disabling all
		// built-ins. A one-element slice makes the SDK emit `--tools ""`;
		// a nil/empty slice would omit the flag and leave all tools available.
		tools = []string{""}
	}
	return &claude.RunOptions{
		Format:         format,
		SystemPrompt:   b.systemPrompt,
		PermissionMode: mode,
		Tools:          tools,
		// Empty until the first turn captures a session; once set, the SDK emits
		// --resume so the CLI owns history server-side. The system prompt is still
		// passed on resume (harmless; dropping flattened history is the win).
		ResumeID: b.sessionID,
	}
}

// ThinkStream sends input to Claude and returns a channel of streaming message chunks.
// Each message on the channel may contain partial text.
func (b *Brain) ThinkStream(ctx context.Context, input string, streamOpts StreamOptions) (*Stream, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		resuming := b.sessionID != ""
		raw, streamedAny, err := b.streamAttempt(ctx, out, streamOpts)
		// A stale/rejected --resume session errors before any text streams. Drop
		// the id and retry once via the flatten path so one bad CLI session file
		// can't wedge the conversation. Guard on !streamedAny to avoid re-speaking
		// already-emitted audio, and on ctx.Err()==nil so a user barge-in cancel
		// is not mistaken for a resume failure.
		if err != nil && resuming && !streamedAny && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "samantha: claude resume failed, retrying without session: %v\n", err)
			b.dropSession()
			raw, _, err = b.streamAttempt(ctx, out, streamOpts)
		}
		if err != nil {
			done <- StreamResult{Err: err}
			return
		}

		response, finErr := finalizeStreamedText(ctx, out, raw)
		if finErr != nil {
			done <- StreamResult{Err: finErr}
			return
		}
		b.history = append(b.history, Turn{Role: "samantha", Content: response})
		b.trimHistory()
		// Checked after the turn lands so a reset never costs the turn in flight.
		b.enforceSessionBudget()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// enforceSessionBudget drops the CLI session once the transcript it replays
// grows past claude_max_session_tokens. Resuming hands the whole session back to
// the model every turn, so prompt size — and with it TTFT and cache-write cost —
// climbs for as long as the conversation lives. Dropping the id makes the next
// turn start a fresh CLI session from flattened history (the same bounded window
// the pre-resume code always sent), which re-floors the cost.
//
// Local history is untouched: the reset changes only which prefix the model
// sees, never what the user said.
func (b *Brain) enforceSessionBudget() {
	budget := b.cfg.ClaudeMaxSessionTokens
	if budget <= 0 || b.sessionID == "" || b.promptTokens < budget {
		return
	}
	// Log every reset: each one floors the model back to the flatten window and
	// is a real context change the user should be able to see in the log.
	fmt.Fprintf(os.Stderr, "samantha: claude session past claude_max_session_tokens=%d; starting a fresh session from recent history\n", budget)
	b.dropSession()
}

// dropSession clears the CLI resume id and the budget counter. Call whenever the
// session is intentionally abandoned (budget reset, clear/load history, or a
// rejected --resume), so a stale high promptTokens cannot force an immediate
// re-reset on the next successful turn.
func (b *Brain) dropSession() {
	b.sessionID = ""
	b.promptTokens = 0
}

// streamAttempt runs a single Claude streaming turn: it builds the prompt for
// the current session state, forwards assistant text to out, and captures the
// CLI session id for resume. It returns the raw (uncleaned) assistant text,
// whether any chunk reached out, and any terminal stream error. It does not
// touch b.history or the fallback text — the caller finalizes once, after any
// resume-failure retry, so a failed first attempt never emits the fallback.
func (b *Brain) streamAttempt(ctx context.Context, out chan<- string, streamOpts StreamOptions) (string, bool, error) {
	prompt := b.buildPrompt()

	// Partial (stream_event) messages are not requested: chunking is
	// per-assistant-message, so deltas would be discarded anyway.
	opts := b.runOptions(claude.StreamJSONOutput, streamOpts.ToolsEnabled)

	messages, errs := b.client.StreamPrompt(ctx, prompt, opts)

	var fullResponse strings.Builder
	streamedAny := false
	var streamErr error
	var resultErr error

	for msg := range messages {
		// The CLI stamps session_id on its init and result messages; capture it
		// from whichever arrives so the next turn can resume.
		if msg.SessionID != "" {
			b.sessionID = msg.SessionID
		}

		// Extract text content from assistant messages
		if msg.Type == "assistant" {
			var content struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				// The CLI stamps per-request usage on the assistant message. The
				// SDK keeps it because Message is raw JSON; the result message's
				// copy is dropped by the SDK's typed struct, so read it here.
				Usage *claudeUsage `json:"usage"`
			}
			if err := json.Unmarshal(msg.Message, &content); err == nil {
				if u := content.Usage; u != nil {
					// Budget watches the full prompt (input + cache reads +
					// cache writes): that is what --resume replays next turn.
					if p := u.prompt(); p > 0 {
						b.promptTokens = p
					}
					// OnUsage "prefill" is the tokens the server actually
					// processed this turn — exclude cache reads so a warm
					// prefix still shows a small prefill and cache regressions
					// stay visible in the activity feed.
					prefill := u.InputTokens + u.CacheCreationTokens
					if streamOpts.OnUsage != nil && prefill+u.OutputTokens > 0 {
						streamOpts.OnUsage(prefill, u.OutputTokens)
					}
				}
				for _, c := range content.Content {
					if c.Type == "text" && c.Text != "" {
						fullResponse.WriteString(c.Text)
						if err := sendChunk(ctx, out, c.Text); err != nil {
							return fullResponse.String(), streamedAny, err
						}
						streamedAny = true
					}
				}
			}
		}

		// Check for final result
		if msg.Type == "result" {
			// The CLI can report failure with exit 0 and is_error on the result
			// (max turns, execution error, some session rejections). Without this
			// the error text would be spoken as the reply and a stale --resume
			// would never reach the flatten retry.
			if msg.IsError && fullResponse.Len() == 0 {
				resultErr = resultError(msg.Subtype, msg.Result)
			}
			if msg.Result != "" && fullResponse.Len() == 0 && !msg.IsError {
				fullResponse.WriteString(msg.Result)
				if err := sendChunk(ctx, out, msg.Result); err != nil {
					return fullResponse.String(), streamedAny, err
				}
				streamedAny = true
			}
		}
	}

	// Drain errors
	for err := range errs {
		if err != nil {
			if streamErr == nil {
				streamErr = fmt.Errorf("claude stream: %w", err)
			}
		}
	}
	if streamErr == nil {
		streamErr = resultErr
	}

	return fullResponse.String(), streamedAny, streamErr
}

// resultError turns a claude result message/transcript marked is_error into a Go
// error, preserving the CLI's own text so the log and degraded path can show it.
func resultError(subtype, text string) error {
	detail := strings.TrimSpace(text)
	switch {
	case subtype != "" && detail != "":
		return fmt.Errorf("claude result error (%s): %s", subtype, detail)
	case subtype != "":
		return fmt.Errorf("claude result error (%s)", subtype)
	case detail != "":
		return fmt.Errorf("claude result error: %s", detail)
	}
	return fmt.Errorf("claude result error")
}

// ThinkFull sends input and waits for the complete response.
func (b *Brain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})

	resuming := b.sessionID != ""
	response, err := b.thinkFullAttempt(ctx, streamOpts)
	// Same stale-session guard as ThinkStream: a rejected --resume fails the
	// whole call, so drop the id and retry once via the flatten path. ctx.Err()
	// gate keeps a user cancellation from being treated as a resume failure.
	if err != nil && resuming && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "samantha: claude resume failed, retrying without session: %v\n", err)
		b.dropSession()
		response, err = b.thinkFullAttempt(ctx, streamOpts)
	}
	if err != nil {
		return "", err
	}

	b.history = append(b.history, Turn{Role: "samantha", Content: response})
	b.trimHistory()
	// ClaudeResult has no usage fields, so estimate the replayed session size
	// from local history and enforce the same cap as ThinkStream. Stream turns
	// overwrite promptTokens with real CLI usage when available.
	b.promptTokens = b.estimateSessionTokens()
	b.enforceSessionBudget()

	return response, nil
}

// estimateSessionTokens approximates the CLI-replayed prompt size when usage is
// unavailable (blocking path). chars/4 is a coarse but stable stand-in for
// Anthropic tokenization; the stream path replaces it with real numbers.
func (b *Brain) estimateSessionTokens() int {
	n := len(b.systemPrompt)
	for _, t := range b.history {
		n += len(t.Content)
	}
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// thinkFullAttempt runs a single blocking Claude turn and captures the session
// id for resume. JSON output is required here (not plain text): the SDK only
// populates ClaudeResult.SessionID when it parses a JSON transcript.
func (b *Brain) thinkFullAttempt(ctx context.Context, streamOpts StreamOptions) (string, error) {
	prompt := b.buildPrompt()
	opts := b.runOptions(claude.JSONOutput, streamOpts.ToolsEnabled)

	result, err := b.client.RunPromptCtx(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("claude error: %w", err)
	}
	if result.SessionID != "" {
		b.sessionID = result.SessionID
	}
	// Same exit-0 failure shape as the streaming path: never speak the CLI's
	// error text, and let a rejected --resume fall through to the flatten retry.
	if result.IsError {
		return "", resultError(result.Subtype, result.Result)
	}

	// Clean first, then fall back, so the fallback is spoken verbatim.
	response := cleanForVoice(result.Result)
	if response == "" {
		response = fallbackResponse
	}
	return response, nil
}

func (b *Brain) buildPrompt() string {
	var parts []string

	// Only prepend flattened history when no CLI session carries it. With a
	// resume id the CLI owns history server-side, so re-sending it would bust
	// the cached prefix — send only the new turn (plus the turn instruction).
	if b.sessionID == "" {
		recent := b.history
		if len(recent) > 6 {
			recent = recent[len(recent)-6:]
		}

		if len(recent) > 1 {
			parts = append(parts, "Recent conversation:")
			for _, t := range recent[:len(recent)-1] {
				speaker := "User"
				if t.Role == "samantha" {
					speaker = b.cfg.AgentName
				}
				parts = append(parts, fmt.Sprintf("%s: %s", speaker, t.Content))
			}
			parts = append(parts, "")
		}
	}

	parts = append(parts, fmt.Sprintf("User: %s", b.history[len(b.history)-1].Content))
	parts = append(parts, "")
	parts = append(parts, b.turnInstruction)

	return strings.Join(parts, "\n")
}

func (b *Brain) trimHistory() {
	// High/low water trim: drop nothing until MaxHistory*2, then cut to
	// MaxHistory, so the retained prefix stays stable between trims instead
	// of sliding every turn.
	if len(b.history) > b.cfg.MaxHistory*2 {
		b.history = b.history[len(b.history)-b.cfg.MaxHistory:]
	}
}

// History returns the conversation history.
func (b *Brain) History() []Turn {
	return b.history
}

// ClearHistory wipes conversation history. The CLI session id is dropped too so
// a fresh conversation starts a fresh CLI session instead of resuming the old
// one.
func (b *Brain) ClearHistory() {
	b.history = nil
	b.dropSession()
}

// LoadHistory restores conversation history from a saved samantha session. No
// live CLI session exists for persisted history, so the id is cleared: the
// first turn after load runs the flatten path, then captures a new session id
// and resumes from there.
func (b *Brain) LoadHistory(turns []Turn) {
	b.history = normalizePromptHistory(turns)
	b.dropSession()
}

// normalizePromptHistory maps persisted roles onto the prompt-based providers'
// native scheme: "assistant" becomes "samantha" and tool results (which only
// ollama produces) are dropped.
func normalizePromptHistory(turns []Turn) []Turn {
	out := make([]Turn, 0, len(turns))
	for _, t := range turns {
		switch t.Role {
		case "tool":
			continue
		case "assistant":
			t.Role = "samantha"
		}
		out = append(out, t)
	}
	return out
}

// fallbackResponse is spoken verbatim when a provider returns nothing; it must
// be substituted after cleanForVoice, which would strip its "Hmm, " prefix.
const fallbackResponse = "Hmm, I lost my train of thought for a second. What were you saying?"

// RecoveryReply is spoken when a turn dies on a hard brain or tool error, so
// the user always hears the loop close instead of silence. Shared with the
// pipeline's degraded-turn path; the error detail goes to the activity feed.
const RecoveryReply = "I hit an error while working on that. Want me to try a simpler approach?"

var (
	markdownReplacer = strings.NewReplacer("**", "", "```", "", "##", "", "# ", "")
	// Vocal fillers that TTS spells out instead of vocalizing. Whole words only,
	// plus a trailing comma so "Hmm, hello" cleans to "hello".
	fillerRE = regexp.MustCompile(`(?i)\b(?:hmm+|umm+|uhh+|ahh+|mmm+|haha|heh)\b,?\s*`)
)

func cleanForVoice(s string) string {
	s = fillerRE.ReplaceAllString(markdownReplacer.Replace(s), "")
	return strings.TrimSpace(textclean.StripUnsupportedKokoroMarks(s))
}

// finalizeStreamedText cleans a streamed assistant reply and guarantees a
// speakable/displayable string. When the model finished tool calls (or the
// stream) with no usable text, the canned fallback is streamed so the TUI and
// TTS do not silently end the turn with an empty ResponseReady.
func finalizeStreamedText(ctx context.Context, out chan<- string, raw string) (string, error) {
	cleaned := cleanForVoice(raw)
	if cleaned != "" {
		return cleaned, nil
	}
	// Nothing usable: stream the fallback only when no raw text was produced
	// (tool-only turns). If raw had content that cleaning removed, the TUI
	// already showed those deltas — still record the fallback in history so
	// the next turn is not left without an assistant message.
	if strings.TrimSpace(raw) == "" {
		if err := sendChunk(ctx, out, fallbackResponse); err != nil {
			return "", err
		}
	}
	return fallbackResponse, nil
}
