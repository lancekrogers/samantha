package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestCompactInstructionResolvesEmbeddedDefault(t *testing.T) {
	cfg := &config.Config{AgentName: "Samantha", PromptsDir: t.TempDir()}
	text, err := CompactInstruction(cfg)
	if err != nil {
		t.Fatalf("CompactInstruction() error = %v", err)
	}
	if !strings.Contains(text, "Samantha") {
		t.Fatalf("compact prompt did not substitute {agent_name}:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "summar") {
		t.Fatalf("compact prompt does not instruct summarizing:\n%s", text)
	}
}

// The turn instruction is voice framing ("2–3 sentences max"), and buildPrompt
// appends it last — after whatever the caller asked for. A compact turn asks for
// a dense briefing, so it must be the model's last instruction: without the
// suppression the summary that reseeds the whole conversation gets truncated to
// a spoken-length blurb.
func TestOmitTurnInstructionDropsVoiceFramingFromPrompt(t *testing.T) {
	const compactPrompt = "Summarize this entire conversation so far."

	t.Run("claude", func(t *testing.T) {
		fake := &fakeClaudeClient{
			fullResults: []*claude.ClaudeResult{{Result: "a summary"}, {Result: "a summary"}},
		}
		b := newTestBrain(fake)
		b.LoadHistory(seededHistory())

		if _, err := b.ThinkFull(context.Background(), compactPrompt, StreamOptions{OmitTurnInstruction: true}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		if got := fake.runPrompts[0]; strings.Contains(got, b.turnInstruction) {
			t.Fatalf("compact prompt still carries the turn instruction %q:\n%s", b.turnInstruction, got)
		}
		if got := fake.runPrompts[0]; !strings.HasSuffix(got, compactPrompt) {
			t.Fatalf("compact instruction must be the model's last word:\n%s", got)
		}

		// A normal turn still gets it — the flag is opt-in, not a removal.
		if _, err := b.ThinkFull(context.Background(), "how are you", StreamOptions{}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		if got := fake.runPrompts[1]; !strings.Contains(got, b.turnInstruction) {
			t.Fatalf("normal turn lost the turn instruction:\n%s", got)
		}
	})

	t.Run("grok", func(t *testing.T) {
		fake := &fakeGrokClient{
			fullResults: []*grok.GrokResult{{Text: "a summary"}, {Text: "a summary"}},
		}
		g := newTestGrokBrain(fake)
		g.LoadHistory(seededHistory())

		if _, err := g.ThinkFull(context.Background(), compactPrompt, StreamOptions{OmitTurnInstruction: true}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		if got := fake.runPrompts[0]; strings.Contains(got, g.turnInstruction) {
			t.Fatalf("compact prompt still carries the turn instruction %q:\n%s", g.turnInstruction, got)
		}
		if got := fake.runPrompts[0]; !strings.HasSuffix(got, compactPrompt) {
			t.Fatalf("compact instruction must be the model's last word:\n%s", got)
		}

		if _, err := g.ThinkFull(context.Background(), "how are you", StreamOptions{}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		if got := fake.runPrompts[1]; !strings.Contains(got, g.turnInstruction) {
			t.Fatalf("normal turn lost the turn instruction:\n%s", got)
		}
	})
}
