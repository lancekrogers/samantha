package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/skills"
)

// OllamaBrain implements Provider using the Ollama API with tool calling.
type OllamaBrain struct {
	client       *api.Client
	model        string
	workDir      string
	history      []api.Message
	cfg          *config.Config
	systemPrompt string
	skills       []skills.Skill
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

	// Verify model exists. The probe uses its own timeout-bounded client so a
	// reachable-but-hung host can't block startup; chat requests keep the
	// untimed default client since generations can run long.
	probe := api.NewClient(base, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := probe.List(ctx)
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

	workDir, _ := os.Getwd()

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}

	catalog, err := loadSkillsCatalog(context.Background(), cfg, workDir)
	if err != nil {
		return nil, err
	}

	return &OllamaBrain{
		client:       client,
		model:        cfg.OllamaModel,
		workDir:      workDir,
		cfg:          cfg,
		systemPrompt: systemPrompt,
		skills:       catalog,
	}, nil
}

// loadSkillsCatalog returns the Agent Skills catalog when skills_enabled is
// true; otherwise an empty catalog. Discovery follows the cross-client
// .agents/skills convention (project then user) plus the configured Samantha
// skills_dir. .claude/skills is intentionally not scanned — Claude Code owns
// that path. Missing dirs yield empty contributions, not errors.
func loadSkillsCatalog(ctx context.Context, cfg *config.Config, workDir string) ([]skills.Skill, error) {
	if cfg == nil || !cfg.SkillsEnabled {
		return nil, nil
	}
	configured := cfg.SkillsDir
	if configured == "" {
		configured = config.SkillsDir()
	}
	dirs := skills.DefaultSearchPaths(workDir, configured)
	catalog, err := skills.Loader{Dirs: dirs}.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading skills: %w", err)
	}
	return catalog, nil
}

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		// Per-turn tool session: tracks active skill for progressive disclosure.
		sess := &toolSession{
			catalog:        o.skills,
			commandTimeout: time.Duration(config.ClampToolCommandTimeout(o.cfg.ToolCommandTimeout)) * time.Second,
			onStart:        opts.OnToolStart,
			onEnd:          opts.OnToolEnd,
		}

		for i := 0; i < maxToolIterations; i++ {
			var tools api.Tools
			if opts.ToolsEnabled {
				tools = sess.tools()
			}

			messages := o.buildMessages()
			stream := true
			req := &api.ChatRequest{
				Model:    o.model,
				Messages: messages,
				Tools:    tools,
				Stream:   &stream,
			}

			// Accumulate the full response (text + tool calls).
			var textBuf strings.Builder
			var toolCalls []api.ToolCall

			err := o.chat(ctx, req, func(resp api.ChatResponse) error {
				if resp.Message.Content != "" {
					textBuf.WriteString(resp.Message.Content)
					// Stream every content delta as it arrives, tools enabled or
					// not, so the TUI renders token-by-token. Any preamble a
					// tool-calling iteration emits streams too; the final answer
					// continues the same reply after tools run.
					if err := sendChunk(ctx, out, resp.Message.Content); err != nil {
						return err
					}
				}
				if len(resp.Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, resp.Message.ToolCalls...)
				}
				return nil
			})
			if err != nil {
				done <- StreamResult{Err: fmt.Errorf("ollama stream: %w", err)}
				return
			}

			// If model made tool calls, execute them and loop.
			if len(toolCalls) > 0 {
				// Add the assistant's tool-calling message to history.
				o.history = append(o.history, api.Message{
					Role:      "assistant",
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				})

				// Execute each tool and add results (skills + full CLI tools).
				for _, tc := range toolCalls {
					result := sess.execute(ctx, o.workDir, tc)
					o.history = append(o.history, api.Message{
						Role:    "tool",
						Content: result,
					})
				}
				continue // re-request with tool results
			}

			// No tool calls — the answer already streamed above; record a
			// cleaned form in history. Tool-only turns often finish with an
			// empty final message; finalizeStreamedText streams a fallback so
			// the UI never ends on "looking into it" with silence.
			response, err := finalizeStreamedText(ctx, out, textBuf.String())
			if err != nil {
				done <- StreamResult{Err: err}
				return
			}
			o.history = append(o.history, api.Message{Role: "assistant", Content: response})
			o.trimHistory()
			done <- StreamResult{}
			return
		}

		if err := sendChunk(ctx, out, "I seem to be going in circles with my tools. Let me just answer directly."); err != nil {
			done <- StreamResult{Err: err}
			return
		}
		o.history = append(o.history, api.Message{
			Role:    "assistant",
			Content: "I seem to be going in circles with my tools. Let me just answer directly.",
		})
		o.trimHistory()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

func sendChunk(ctx context.Context, out chan<- string, chunk string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- chunk:
		return nil
	}
}

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error) {
	o.history = append(o.history, api.Message{Role: "user", Content: input})

	sess := &toolSession{
		catalog:        o.skills,
		commandTimeout: time.Duration(config.ClampToolCommandTimeout(o.cfg.ToolCommandTimeout)) * time.Second,
		onStart:        opts.OnToolStart,
		onEnd:          opts.OnToolEnd,
	}

	for i := 0; i < maxToolIterations; i++ {
		var tools api.Tools
		if opts.ToolsEnabled {
			tools = sess.tools()
		}

		messages := o.buildMessages()
		stream := false
		req := &api.ChatRequest{
			Model:    o.model,
			Messages: messages,
			Tools:    tools,
			Stream:   &stream,
		}

		var response api.Message
		err := o.chat(ctx, req, func(resp api.ChatResponse) error {
			response = resp.Message
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("ollama error: %w", err)
		}

		// Tool calls — execute and loop (skills stack on full CLI tools).
		if len(response.ToolCalls) > 0 {
			o.history = append(o.history, api.Message{
				Role:      "assistant",
				Content:   response.Content,
				ToolCalls: response.ToolCalls,
			})

			for _, tc := range response.ToolCalls {
				result := sess.execute(ctx, o.workDir, tc)
				o.history = append(o.history, api.Message{
					Role:    "tool",
					Content: result,
				})
			}
			continue
		}

		// Text response. Clean first, then fall back, so the fallback is spoken verbatim.
		text := cleanForVoice(response.Content)
		if text == "" {
			text = fallbackResponse
		}
		o.history = append(o.history, api.Message{Role: "assistant", Content: text})
		o.trimHistory()
		return text, nil
	}

	return "I seem to be going in circles with my tools. Let me just answer directly.", nil
}

// chat issues a chat request, retrying once without tools if the model reports
// it doesn't support them — so a non-tool model degrades to plain chat instead
// of failing the turn. The degradation is logged so "tools silently vanished"
// is never invisible.
func (o *OllamaBrain) chat(ctx context.Context, req *api.ChatRequest, fn api.ChatResponseFunc) error {
	err := o.client.Chat(ctx, req, fn)
	if err != nil && req.Tools != nil && modelRejectedTools(err) {
		fmt.Fprintf(os.Stderr, "samantha: ollama model %q rejected tools (%v); continuing without tool calling — file read/write will not run\n", o.model, err)
		req.Tools = nil
		return o.client.Chat(ctx, req, fn)
	}
	return err
}

func modelRejectedTools(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not support tools")
}

// Warmup preloads the model into memory with a minimal request so the user's
// first real turn doesn't pay the cold-start (model-load) cost. Best-effort:
// it caps generation, sends no tools, and ignores all errors so it can never
// block or disrupt startup.
func (o *OllamaBrain) Warmup(ctx context.Context) {
	stream := false
	req := &api.ChatRequest{
		Model:    o.model,
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Stream:   &stream,
		Options:  map[string]any{"num_predict": 1},
	}
	_ = o.client.Chat(ctx, req, func(api.ChatResponse) error { return nil })
}

// ClearHistory wipes conversation history.
func (o *OllamaBrain) ClearHistory() {
	o.history = nil
}

// History returns conversation history as Turn slices for session persistence.
func (o *OllamaBrain) History() []Turn {
	turns := make([]Turn, len(o.history))
	for i, m := range o.history {
		turns[i] = Turn{Role: m.Role, Content: m.Content}
	}
	return turns
}

// LoadHistory restores conversation history from a saved session. Sessions
// saved by the prompt-based providers use role "samantha" for replies; map it
// to ollama's native "assistant".
func (o *OllamaBrain) LoadHistory(turns []Turn) {
	o.history = make([]api.Message, len(turns))
	for i, t := range turns {
		role := t.Role
		if role == "samantha" {
			role = "assistant"
		}
		o.history[i] = api.Message{Role: role, Content: t.Content}
	}
}

func (o *OllamaBrain) buildMessages() []api.Message {
	systemPrompt := o.systemPrompt + "\n" + EnvironmentContext(o.workDir)
	if sc := SkillContext(o.skills); sc != "" {
		systemPrompt += sc
	}

	msgs := []api.Message{
		{Role: "system", Content: systemPrompt},
	}

	recent := o.history[historyWindowStart(o.history, o.cfg.MaxHistory*2):]

	msgs = append(msgs, recent...)
	return msgs
}

func (o *OllamaBrain) trimHistory() {
	if start := historyWindowStart(o.history, o.cfg.MaxHistory*2); start > 0 {
		o.history = o.history[start:]
	}
}

// historyWindowStart returns the index where a history window of at most max
// messages begins. If the tail slice would open on a tool result — stranding
// it from its assistant tool-call antecedent — the window advances to the
// next user message.
func historyWindowStart(history []api.Message, max int) int {
	if max <= 0 {
		return len(history)
	}
	start := 0
	if len(history) > max {
		start = len(history) - max
	}
	if start == 0 || history[start].Role != "tool" {
		return start
	}
	for i := start + 1; i < len(history); i++ {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history)
}
