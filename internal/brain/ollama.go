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
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/skills"
)

// ollamaSpeakerSep marks a stable speaker id prefix on stored user messages so
// History/LoadHistory can round-trip Turn.Speaker without changing the Ollama
// wire format's role field. ASCII record separator is invalid in speech text.
const ollamaSpeakerSep = "\x1e"

// OllamaBrain implements Provider using the Ollama API with tool calling.
type OllamaBrain struct {
	client       *api.Client
	model        string
	workDir      string
	history      []api.Message
	cfg          *config.Config
	systemPrompt string
	// fullSystemPrompt is assembled once at construction (persona prompt +
	// environment + skills catalog) and sent byte-for-byte on every request,
	// so Ollama's KV prefix cache survives across turns. Per-turn skill
	// activations are spliced onto the current user message instead.
	fullSystemPrompt string
	// personaReloader re-reads the persona document each turn so an edit lands
	// on the next reply. The assembled prompt is only rebuilt when the document
	// actually changed, which keeps the server's prefix cache intact on the
	// overwhelmingly common no-change path.
	personaReloader   *promptReloader
	keepAlive         *api.Duration
	skills            []skills.Skill
	skillRouter       *semanticSkillRouter
	skillRouterWarned bool
	budgetWarned      bool
	// speakerNames resolves stable speaker ids when building chat messages.
	speakerNames SpeakerNames
}

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (o *OllamaBrain) SetSpeakerNames(names SpeakerNames) { o.speakerNames = names }

func encodeOllamaUser(speakerID, content string) string {
	speakerID = strings.TrimSpace(speakerID)
	if speakerID == "" {
		return content
	}
	return speakerID + ollamaSpeakerSep + content
}

func decodeOllamaUser(stored string) (speakerID, content string) {
	if i := strings.IndexByte(stored, ollamaSpeakerSep[0]); i >= 0 {
		return stored[:i], stored[i+1:]
	}
	return "", stored
}

func (o *OllamaBrain) formatStoredUser(stored string) string {
	speakerID, content := decodeOllamaUser(stored)
	if speakerID == "" {
		return content
	}
	return FormatUserLine(speakerID, content, o.speakerNames)
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
	embedCtx, embedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	router, routerErr := newSemanticSkillRouter(embedCtx, client, cfg.OllamaEmbeddingModel, cfg.SkillsSimilarityThreshold, catalog)
	embedCancel()
	if routerErr != nil {
		fmt.Fprintf(os.Stderr, "samantha: semantic skill routing unavailable (%v); continuing with the Agent Skills catalog\n", routerErr)
	}

	var keepAlive *api.Duration
	if s := strings.TrimSpace(cfg.OllamaKeepAlive); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			keepAlive = &api.Duration{Duration: d}
		} else {
			fmt.Fprintf(os.Stderr, "samantha: invalid ollama_keep_alive %q (%v); using the server default\n", s, err)
		}
	}

	return &OllamaBrain{
		client:           client,
		model:            cfg.OllamaModel,
		workDir:          workDir,
		cfg:              cfg,
		systemPrompt:     systemPrompt,
		fullSystemPrompt: assembleSystemPrompt(systemPrompt, workDir, catalog),
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, systemPrompt, func(hash string) {
			fmt.Fprintf(os.Stderr, "samantha: persona prompt changed (hash %s)\n", hash)
		}),
		keepAlive:         keepAlive,
		skills:            catalog,
		skillRouter:       router,
		skillRouterWarned: routerErr != nil,
	}, nil
}

// loadSkillsCatalog returns the Agent Skills catalog when skills_enabled is
// true; otherwise an empty catalog. Discovery follows the cross-client
// .agents/skills convention (project, nearest workspace ancestor, then user)
// and then checks the configured Samantha skills_dir. .claude/skills is
// intentionally not scanned — Claude Code owns that path. Missing dirs yield
// empty contributions, not errors.
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
	o.refreshSystemPrompt(opts.OnPromptWarn)
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	o.history = append(o.history, api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)})
	o.ensureContextBudget(skillCtx)

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		// Per-turn tool session: exposes skills through progressive disclosure.
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

			req := o.newChatRequest(o.buildMessages(skillCtx), tools)

			// Accumulate the full response (text + tool calls).
			var textBuf strings.Builder
			var toolCalls []api.ToolCall
			var prefillTokens, genTokens int

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
				if resp.Done {
					prefillTokens, genTokens = resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount
				}
				return nil
			})
			if opts.OnUsage != nil && prefillTokens+genTokens > 0 {
				opts.OnUsage(prefillTokens, genTokens)
			}
			if err != nil {
				err = fmt.Errorf("ollama stream: %w", err)
				// A dead context means shutdown or barge-in — no one is
				// listening for a recovery line. Otherwise close the loop out
				// loud: stream the recovery reply, record it so the next turn
				// has an assistant message, and surface err as detail only.
				if ctx.Err() != nil || sendChunk(ctx, out, RecoveryReply) != nil {
					done <- StreamResult{Err: err}
					return
				}
				// Keep any partial streamed text: the user already saw/heard
				// it, so the next turn's context must include it too.
				reply := RecoveryReply
				if partial := strings.TrimSpace(textBuf.String()); partial != "" {
					reply = partial + "\n\n" + RecoveryReply
				}
				o.history = append(o.history, api.Message{Role: "assistant", Content: reply})
				o.trimHistory()
				done <- StreamResult{Err: err, Recovered: true}
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
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	// A failed turn rolls back to this snapshot rather than truncating: the tool
	// loop appends assistant/tool turns and the context budget can trim the
	// front, so length alone cannot describe the pre-call history. Unlike
	// ThinkStream — which records a recovery reply and keeps the turn — a failed
	// ThinkFull answers nothing, so its input must not stay behind.
	restore := append([]api.Message(nil), o.history...)
	o.history = append(o.history, api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)})
	o.ensureContextBudget(skillCtx)

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

		req := o.newChatRequest(o.buildMessages(skillCtx), tools)
		stream := false
		req.Stream = &stream

		var response api.Message
		err := o.chat(ctx, req, func(resp api.ChatResponse) error {
			response = resp.Message
			return nil
		})
		if err != nil {
			o.history = restore
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

// newChatRequest builds a streaming chat request carrying the session's
// context policy: an explicit num_ctx so the server never silently truncates
// at its own default (top truncation eats the system prompt — and the
// persona — first), and keep_alive so the model stays resident between voice
// turns (a reload is a multi-second mid-conversation stall).
func (o *OllamaBrain) newChatRequest(messages []api.Message, tools api.Tools) *api.ChatRequest {
	stream := true
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  messages,
		Tools:     tools,
		Stream:    &stream,
		KeepAlive: o.keepAlive,
	}
	if o.cfg.OllamaNumCtx > 0 {
		req.Options = map[string]any{"num_ctx": o.cfg.OllamaNumCtx}
	}
	return req
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

// SessionInfo reports the in-process conversation for /session display:
// samantha owns messages[], so the size shown is the estimated next request.
func (o *OllamaBrain) SessionInfo() SessionState {
	return SessionState{
		Kind:         SessionKindChat,
		PromptTokens: o.estimateTokens(""),
		Turns:        len(o.history),
	}
}

// History returns conversation history as Turn slices for session persistence.
func (o *OllamaBrain) History() []Turn {
	turns := make([]Turn, len(o.history))
	for i, m := range o.history {
		content, speaker := m.Content, ""
		if m.Role == "user" {
			speaker, content = decodeOllamaUser(m.Content)
		}
		turns[i] = Turn{Role: m.Role, Content: content, Speaker: speaker}
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
		content := t.Content
		if role == "user" && strings.TrimSpace(t.Speaker) != "" {
			content = encodeOllamaUser(t.Speaker, t.Content)
		}
		o.history[i] = api.Message{Role: role, Content: content}
	}
}

// refreshSystemPrompt re-resolves the persona document and rebuilds the
// assembled system prompt only if it changed.
//
// The rebuild is conditional for a concrete reason: buildMessages sends
// fullSystemPrompt as the leading message, and ollama caches the prefix of a
// conversation server-side. Emitting a different system prompt invalidates that
// cache and re-processes the whole transcript. So an unchanged persona keeps the
// exact same string, and a genuinely edited one pays the invalidation once —
// which is correct, because the user did change what the model should see.
func (o *OllamaBrain) refreshSystemPrompt(onWarn func(message string)) {
	persona, changed, err := o.personaReloader.resolve(o.cfg)
	if err != nil && onWarn != nil {
		onWarn(err.Error())
	}
	if !changed {
		return
	}
	o.systemPrompt = persona
	o.fullSystemPrompt = assembleSystemPrompt(persona, o.workDir, o.skills)
}

// assembleSystemPrompt builds the session-stable system prompt: persona
// prompt, environment grounding, and the Tier-1 skills catalog. It runs once
// per brain — the result must stay byte-identical across a session's requests.
func assembleSystemPrompt(personaPrompt, workDir string, catalog []skills.Skill) string {
	full := personaPrompt + "\n" + EnvironmentContext(workDir)
	if sc := SkillContext(catalog); sc != "" {
		full += sc
	}
	return full
}

// buildMessages assembles one chat request: the frozen system prompt, the
// full retained history (trimHistory owns the bound), and — when skills
// activated for this turn — their context spliced onto the latest user
// message. Splicing at the tail keeps every earlier token byte-identical
// across requests, so the server's prefix cache re-processes only the new
// turn instead of the whole transcript.
func (o *OllamaBrain) buildMessages(skillCtx string) []api.Message {
	msgs := make([]api.Message, 0, len(o.history)+1)
	msgs = append(msgs, api.Message{Role: "system", Content: o.fullSystemPrompt})
	for _, m := range o.history {
		if m.Role == "user" {
			m.Content = o.formatStoredUser(m.Content)
		}
		msgs = append(msgs, m)
	}
	if skillCtx != "" {
		for i := len(msgs) - 1; i > 0; i-- {
			if msgs[i].Role == "user" {
				msgs[i].Content += "\n" + skillCtx
				break
			}
		}
	}
	return msgs
}

// ensureContextBudget permanently drops the oldest history (user-turn
// aligned) when the estimated prompt would overflow ollama_num_ctx. An
// explicit trim beats the server silently truncating from the top, which
// eats the system prompt — and with it the persona — first. The system
// prompt itself is never trimmed: if it alone exceeds the budget, the
// num_ctx setting is what has to change.
func (o *OllamaBrain) ensureContextBudget(skillCtx string) {
	budget := o.cfg.OllamaNumCtx
	if budget <= 0 {
		return
	}
	limit := budget * 9 / 10
	if o.estimateTokens(skillCtx) <= limit {
		return
	}
	for len(o.history) > 2 && o.estimateTokens(skillCtx) > limit {
		start := historyWindowStart(o.history, len(o.history)-2)
		if start <= 0 || start >= len(o.history) {
			break
		}
		o.history = o.history[start:]
	}
	if !o.budgetWarned {
		o.budgetWarned = true
		fmt.Fprintf(os.Stderr, "samantha: prompt near ollama_num_ctx=%d; dropping oldest history to stay under budget\n", budget)
	}
}

// estimateTokens coarsely sizes the next request (≈4 bytes per token plus
// per-message overhead). It only needs to be accurate enough to trim before
// the server truncates.
func (o *OllamaBrain) estimateTokens(skillCtx string) int {
	n := len(o.fullSystemPrompt) + len(skillCtx)
	for _, m := range o.history {
		n += len(m.Content) + 16
	}
	return n / 4
}

func (o *OllamaBrain) routeSkillContext(ctx context.Context, input string, onStart, onEnd func(name, detail string)) string {
	matched, err := o.skillRouter.Match(ctx, input)
	if err != nil {
		if !o.skillRouterWarned {
			fmt.Fprintf(os.Stderr, "samantha: semantic skill routing failed (%v); continuing with the Agent Skills catalog\n", err)
			o.skillRouterWarned = true
		}
		return ""
	}
	if len(matched) > 0 {
		names := make([]string, len(matched))
		for i, skill := range matched {
			names[i] = skill.Name
		}
		if onStart != nil {
			onStart("activate_skill", strings.Join(names, ", "))
		}
		if onEnd != nil {
			onEnd("activate_skill", fmt.Sprintf("injected %d skill(s): %s", len(names), strings.Join(names, ", ")))
		}
	}
	return ActivatedSkillContext(matched)
}

// trimHistory bounds retained history with a high/low water mark instead of
// a per-turn sliding window: nothing is dropped until the count exceeds
// MaxHistory*2, then it cuts down to MaxHistory (user-turn aligned). Between
// trims the retained prefix is byte-stable, so the server's KV prefix cache
// keeps hitting; a slide-per-turn window shifted the prefix every turn and
// forced a full re-prefill for the rest of the conversation.
func (o *OllamaBrain) trimHistory() {
	if len(o.history) <= o.cfg.MaxHistory*2 {
		return
	}
	if start := historyWindowStart(o.history, o.cfg.MaxHistory); start > 0 {
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
