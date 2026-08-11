package brain

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
)

// grokRunner is the slice of the grok client the brain needs: streaming for
// voice/TUI turns and the blocking path for text turns. Narrowing it to an
// interface lets tests inject a fake without shelling out to a real grok CLI.
// *grok.GrokClient satisfies it.
type grokRunner interface {
	StreamPrompt(ctx context.Context, prompt string, opts *grok.RunOptions) (<-chan grok.Event, <-chan error)
	RunPromptCtx(ctx context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error)
}

// GrokBrain manages conversation with Grok via grok-go-sdk, driving the local
// grok CLI the same way the Claude provider drives the claude CLI.
type GrokBrain struct {
	client          grokRunner
	cfg             *config.Config
	systemPrompt    string
	turnInstruction string
	personaReloader *promptReloader
	turnReloader    *promptReloader
	history         []Turn
	// speakerNames resolves stable speaker ids for flatten prompts (optional).
	speakerNames SpeakerNames
	// sessionID is the grok CLI session captured on the first turn and reused
	// via --resume thereafter, so subsequent turns send only the new input.
	// Empty until the first turn captures it; scoped to this GrokBrain, which
	// is constructed per conversation.
	sessionID string
}

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (g *GrokBrain) SetSpeakerNames(names SpeakerNames) { g.speakerNames = names }

// NewGrok creates a Grok brain provider backed by the grok CLI.
func NewGrok(cfg *config.Config) (*GrokBrain, error) {
	client, err := grok.NewClientFromPath()
	if err != nil {
		return nil, fmt.Errorf("grok CLI not found on PATH: %w", err)
	}

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}

	g := &GrokBrain{
		client:          client,
		cfg:             cfg,
		systemPrompt:    systemPrompt,
		turnInstruction: turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, systemPrompt, func(hash string) {
			fmt.Fprintf(os.Stderr, "samantha: persona prompt changed (hash %s)\n", hash)
		}),
		turnReloader: newPromptReloader(prompts.KindTurn, cfg.TurnPrompt, turn, nil),
	}
	g.systemPrompt = g.assembleSystem(systemPrompt)
	return g, nil
}

// Available returns true if the grok CLI can be located.
func (g *GrokBrain) Available() bool {
	_, err := grok.LocateBinary()
	return err == nil
}

// ThinkStream sends input to Grok and returns a channel of streaming text chunks.
// Only spoken "text" events are forwarded; "thought" (reasoning) events are
// dropped so Samantha never voices her chain of thought.
// assembleSystem builds Grok's system prompt through the shared policy, so it
// receives the same machine grounding every other provider does.
func (g *GrokBrain) assembleSystem(persona string) string {
	workDir, _ := os.Getwd()
	return AssembleSystemPrompt(SystemPromptInput{
		Provider: providerGrok,
		Persona:  persona,
		WorkDir:  workDir,
		Cfg:      g.cfg,
	})
}

// refreshPrompts re-resolves the bound persona and turn documents each turn.
func (g *GrokBrain) refreshPrompts(onWarn func(string)) {
	if persona, _, err := g.personaReloader.resolve(g.cfg); err == nil {
		g.systemPrompt = g.assembleSystem(persona)
	} else if onWarn != nil {
		onWarn(err.Error())
	}
	if turn, _, err := g.turnReloader.resolve(g.cfg); err == nil {
		g.turnInstruction = turn
	} else if onWarn != nil {
		onWarn(err.Error())
	}
}

func (g *GrokBrain) ThinkStream(ctx context.Context, input string, streamOpts StreamOptions) (*Stream, error) {
	g.refreshPrompts(streamOpts.OnPromptWarn)
	g.history = append(g.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker})

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		resuming := g.sessionID != ""
		raw, streamedAny, err := g.streamAttempt(ctx, out, streamOpts)
		// A stale/rejected --resume session errors before any text streams. Drop
		// the id and retry once via the flatten path so one bad CLI session file
		// can't wedge the conversation. Guard on !streamedAny to avoid re-speaking
		// already-emitted audio, and on ctx.Err()==nil so a user barge-in cancel
		// is not mistaken for a resume failure.
		if err != nil && resuming && !streamedAny && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "samantha: grok resume failed, retrying without session: %v\n", err)
			g.dropSession()
			raw, _, err = g.streamAttempt(ctx, out, streamOpts)
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
		g.history = append(g.history, Turn{Role: "samantha", Content: response})
		g.trimHistory()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// streamAttempt runs a single Grok streaming turn: it builds the prompt for
// the current session state, forwards spoken text to out, and captures the CLI
// session id for resume. It returns the raw assistant text, whether any chunk
// reached out, and any terminal stream error. The caller finalizes once, after
// any resume-failure retry, so a failed first attempt never emits the fallback.
func (g *GrokBrain) streamAttempt(ctx context.Context, out chan<- string, streamOpts StreamOptions) (string, bool, error) {
	prompt := g.buildPrompt(streamOpts.OmitTurnInstruction)

	events, errs := g.client.StreamPrompt(ctx, prompt, g.runOptions(grok.StreamingJSONOutput, streamOpts.ToolsEnabled))

	var fullResponse strings.Builder
	streamedAny := false
	var streamErr error

	for ev := range events {
		// The CLI stamps sessionId on its events; capture it from whichever
		// arrives so the next turn can resume.
		if ev.SessionID != "" {
			g.sessionID = ev.SessionID
		}

		switch ev.Type {
		case grok.EventText:
			if text := ev.Content(); text != "" {
				fullResponse.WriteString(text)
				if err := sendChunk(ctx, out, text); err != nil {
					return fullResponse.String(), streamedAny, err
				}
				streamedAny = true
			}
		case grok.EventError:
			// An error event before any text is also how a rejected --resume
			// surfaces on exit 0; keeping it terminal lets the retry run.
			if streamErr == nil {
				streamErr = fmt.Errorf("grok stream: %s", strings.TrimSpace(ev.Content()))
			}
		}
	}

	// Drain errors.
	for err := range errs {
		if err != nil && streamErr == nil {
			streamErr = fmt.Errorf("grok stream: %w", err)
		}
	}

	return fullResponse.String(), streamedAny, streamErr
}

// ThinkFull sends input and waits for the complete response.
func (g *GrokBrain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	// Same append-then-roll-back contract as Claude: the prompt comes from
	// history, and a turn that never answered must not leave its input behind.
	restore := len(g.history)
	g.history = append(g.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker})

	resuming := g.sessionID != ""
	response, err := g.thinkFullAttempt(ctx, streamOpts)
	// Same stale-session guard as ThinkStream: a rejected --resume fails the
	// whole call, so drop the id and retry once via the flatten path. ctx.Err()
	// gate keeps a user cancellation from being treated as a resume failure.
	if err != nil && resuming && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "samantha: grok resume failed, retrying without session: %v\n", err)
		g.dropSession()
		response, err = g.thinkFullAttempt(ctx, streamOpts)
	}
	if err != nil {
		g.history = g.history[:restore]
		return "", err
	}

	g.history = append(g.history, Turn{Role: "samantha", Content: response})
	g.trimHistory()

	return response, nil
}

// thinkFullAttempt runs a single blocking Grok turn and captures the session
// id for resume. JSON output is required here (not plain text): the SDK's
// decodeOutput only populates GrokResult.SessionID and IsError when it parses
// a JSON result — plain output returns Text alone.
func (g *GrokBrain) thinkFullAttempt(ctx context.Context, streamOpts StreamOptions) (string, error) {
	prompt := g.buildPrompt(streamOpts.OmitTurnInstruction)

	result, err := g.client.RunPromptCtx(ctx, prompt, g.runOptions(grok.JSONOutput, streamOpts.ToolsEnabled))
	if err != nil {
		return "", fmt.Errorf("grok error: %w", err)
	}
	if result.SessionID != "" {
		g.sessionID = result.SessionID
	}
	// The CLI can report failure with exit 0 and isError on the result (some
	// session rejections included). Never speak the error text, and let a
	// rejected --resume fall through to the flatten retry.
	if result.IsError {
		return "", resultError("grok", result.Subtype, result.Text)
	}

	// Clean first, then fall back, so the fallback is spoken verbatim.
	response := cleanForVoice(result.Text)
	if response == "" {
		response = fallbackResponse
	}
	return response, nil
}

// dropSession clears the CLI resume id. Call whenever the session is
// intentionally abandoned (clear/load history, or a rejected --resume).
func (g *GrokBrain) dropSession() {
	g.sessionID = ""
}

// runOptions builds the grok run options shared by the streaming and blocking
// paths. Tool execution is gated by voice_tools_enabled: disabled removes all
// built-in tools; enabled uses bypassPermissions — which the grok SDK gates
// behind AllowDangerousMode — so hands-free tool calls don't stall on prompts.
func (g *GrokBrain) runOptions(format grok.OutputFormat, toolsEnabled bool) *grok.RunOptions {
	opts := &grok.RunOptions{
		Format:               format,
		SystemPromptOverride: g.systemPrompt,
		// Empty until the first turn captures a session; once set, the SDK
		// emits --resume so the CLI owns history server-side. The system
		// prompt is still passed on resume (harmless; dropping flattened
		// history is the win).
		ResumeID: g.sessionID,
	}
	if toolsEnabled {
		opts.PermissionMode = grok.PermissionBypassPermissions
		opts.AllowDangerousMode = true
	} else {
		// The Grok CLI also uses an empty --tools value to disable every
		// built-in. Keep one empty element so the SDK emits the flag.
		opts.AllowedTools = []string{""}
	}
	if g.cfg.GrokModel != "" {
		opts.Model = g.cfg.GrokModel
	}
	return opts
}

// buildPrompt renders the next turn for the CLI. omitTurnInstruction leaves off
// the per-turn instruction so a meta turn's own instruction is the last thing
// the model reads.
func (g *GrokBrain) buildPrompt(omitTurnInstruction bool) string {
	var parts []string
	agentName := ""
	if g.cfg != nil {
		agentName = g.cfg.AgentName
	}

	// Only prepend flattened history when no CLI session carries it. With a
	// resume id the CLI owns history server-side — send only the new turn
	// (plus the turn instruction).
	if g.sessionID == "" {
		recent := g.history
		if len(recent) > 6 {
			recent = recent[len(recent)-6:]
		}

		if len(recent) > 1 {
			parts = append(parts, "Recent conversation:")
			for _, t := range recent[:len(recent)-1] {
				parts = append(parts, promptUserLine(t, agentName, g.speakerNames))
			}
			parts = append(parts, "")
		}
	}

	last := g.history[len(g.history)-1]
	parts = append(parts, promptUserLine(last, agentName, g.speakerNames))
	if !omitTurnInstruction {
		parts = append(parts, "")
		parts = append(parts, g.turnInstruction)
	}

	return strings.Join(parts, "\n")
}

func (g *GrokBrain) trimHistory() {
	// High/low water trim: drop nothing until MaxHistory*2, then cut to
	// MaxHistory, so the retained prefix stays stable between trims instead
	// of sliding every turn.
	if len(g.history) > g.cfg.MaxHistory*2 {
		g.history = g.history[len(g.history)-g.cfg.MaxHistory:]
	}
}

// History returns the conversation history.
func (g *GrokBrain) History() []Turn {
	return g.history
}

// ClearHistory wipes conversation history. The CLI session id is dropped too
// so a fresh conversation starts a fresh CLI session instead of resuming the
// old one. The on-disk CLI session is left behind — same ghost policy as
// Claude: harmless and never referenced again.
func (g *GrokBrain) ClearHistory() {
	g.history = nil
	g.dropSession()
}

// SessionInfo reports the live CLI session for /session display.
func (g *GrokBrain) SessionInfo() SessionState {
	return SessionState{Kind: SessionKindHarness, ID: g.sessionID, Turns: len(g.history)}
}

// LoadHistory restores conversation history from a saved samantha session. No
// live CLI session exists for persisted history, so the id is cleared: the
// first turn after load runs the flatten path, then captures a new session id
// and resumes from there.
func (g *GrokBrain) LoadHistory(turns []Turn) {
	g.history = normalizePromptHistory(turns)
	g.dropSession()
}
