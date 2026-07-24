package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/textclean"
)

// Brain manages conversation with Claude via claude-code-go.
type Brain struct {
	client          *claude.ClaudeClient
	cfg             *config.Config
	systemPrompt    string
	turnInstruction string
	history         []Turn
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
	}
}

// ThinkStream sends input to Claude and returns a channel of streaming message chunks.
// Each message on the channel may contain partial text.
func (b *Brain) ThinkStream(ctx context.Context, input string, streamOpts StreamOptions) (*Stream, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})
	prompt := b.buildPrompt()

	// Partial (stream_event) messages are not requested: chunking is
	// per-assistant-message, so deltas would be discarded anyway.
	opts := b.runOptions(claude.StreamJSONOutput, streamOpts.ToolsEnabled)

	messages, errs := b.client.StreamPrompt(ctx, prompt, opts)

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		var fullResponse strings.Builder
		var streamErr error

		for msg := range messages {
			// Extract text content from assistant messages
			if msg.Type == "assistant" {
				var content struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(msg.Message, &content); err == nil {
					for _, c := range content.Content {
						if c.Type == "text" && c.Text != "" {
							fullResponse.WriteString(c.Text)
							if err := sendChunk(ctx, out, c.Text); err != nil {
								done <- StreamResult{Err: err}
								return
							}
						}
					}
				}
			}

			// Check for final result
			if msg.Type == "result" && msg.Result != "" {
				if fullResponse.Len() == 0 {
					fullResponse.WriteString(msg.Result)
					if err := sendChunk(ctx, out, msg.Result); err != nil {
						done <- StreamResult{Err: err}
						return
					}
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

		if streamErr != nil {
			done <- StreamResult{Err: streamErr}
			return
		}

		response, finErr := finalizeStreamedText(ctx, out, fullResponse.String())
		if finErr != nil {
			done <- StreamResult{Err: finErr}
			return
		}
		b.history = append(b.history, Turn{Role: "samantha", Content: response})
		b.trimHistory()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// ThinkFull sends input and waits for the complete response.
func (b *Brain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	b.history = append(b.history, Turn{Role: "user", Content: input})
	prompt := b.buildPrompt()

	opts := b.runOptions(claude.TextOutput, streamOpts.ToolsEnabled)

	result, err := b.client.RunPromptCtx(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("claude error: %w", err)
	}

	// Clean first, then fall back, so the fallback is spoken verbatim.
	response := cleanForVoice(result.Result)
	if response == "" {
		response = fallbackResponse
	}

	b.history = append(b.history, Turn{Role: "samantha", Content: response})
	b.trimHistory()

	return response, nil
}

func (b *Brain) buildPrompt() string {
	var parts []string

	// Include recent history for context
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

	parts = append(parts, fmt.Sprintf("User: %s", b.history[len(b.history)-1].Content))
	parts = append(parts, "")
	parts = append(parts, b.turnInstruction)

	return strings.Join(parts, "\n")
}

func (b *Brain) trimHistory() {
	max := b.cfg.MaxHistory * 2
	if len(b.history) > max {
		b.history = b.history[len(b.history)-max:]
	}
}

// History returns the conversation history.
func (b *Brain) History() []Turn {
	return b.history
}

// ClearHistory wipes conversation history.
func (b *Brain) ClearHistory() {
	b.history = nil
}

// LoadHistory restores conversation history from a saved session.
func (b *Brain) LoadHistory(turns []Turn) {
	b.history = normalizePromptHistory(turns)
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
