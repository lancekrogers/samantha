package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

// compactBrain is a stateful fake: ThinkFull records the instruction and
// appends the turn to history the way real providers do, so the test can
// verify the snapshot/tail semantics of CompactConversation.
type compactBrain struct {
	history     []brain.Turn
	summary     string
	fullErr     error
	thinkInputs []string
	thinkOpts   []brain.StreamOptions
	loaded      [][]brain.Turn
}

func (b *compactBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("not used")
}

// ThinkFull models the real provider contract: the input is appended before the
// model call (the prompt is built from history) and rolled back if the call
// fails. A fake that skips the append on the error path cannot observe a
// dangling compact turn, which is what made the failure test a false green.
func (b *compactBrain) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	b.thinkInputs = append(b.thinkInputs, input)
	b.thinkOpts = append(b.thinkOpts, opts)

	restore := len(b.history)
	b.history = append(b.history, brain.Turn{Role: "user", Content: input})

	err := b.fullErr
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		b.history = b.history[:restore]
		return "", err
	}

	b.history = append(b.history, brain.Turn{Role: "samantha", Content: b.summary})
	return b.summary, nil
}

func (b *compactBrain) ClearHistory()         {}
func (b *compactBrain) History() []brain.Turn { return b.history }
func (b *compactBrain) LoadHistory(turns []brain.Turn) {
	b.history = turns
	b.loaded = append(b.loaded, turns)
}

func assertTurns(t *testing.T, got, want []brain.Turn, context string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %+v, want %+v", context, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %+v, want %+v", context, i, got[i], want[i])
		}
	}
}

func compactHistory() []brain.Turn {
	return []brain.Turn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply one"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "reply two"},
	}
}

func TestCompactConversationSummarizesAndReseeds(t *testing.T) {
	b := &compactBrain{history: compactHistory(), summary: "the summary"}
	bus := events.NewBus()
	var compacted []events.ConversationCompacted
	bus.SubscribeAll(func(e events.Event) {
		if c, ok := e.(events.ConversationCompacted); ok {
			compacted = append(compacted, c)
		}
	})

	var backup []brain.Turn
	saves := 0
	p := &Pipeline{
		Brain:           b,
		Events:          bus,
		CompactPrompt:   "SUMMARIZE THE CONVERSATION",
		OnCompactBackup: func(turns []brain.Turn) { backup = turns },
		OnTurn:          func() { saves++ },
	}

	if err := p.CompactConversation(context.Background()); err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}

	if len(b.thinkInputs) != 1 || b.thinkInputs[0] != "SUMMARIZE THE CONVERSATION" {
		t.Fatalf("summarize turn inputs = %v, want the compact prompt once", b.thinkInputs)
	}
	// The briefing has to be the model's last instruction: the per-turn voice
	// instruction ("2–3 sentences max") is appended after it otherwise.
	if !b.thinkOpts[0].OmitTurnInstruction {
		t.Fatal("summarize turn must set OmitTurnInstruction")
	}
	if b.thinkOpts[0].ToolsEnabled {
		t.Fatal("summarize turn must not enable tools")
	}
	// Seed: summary first, then the last exchange verbatim (from the
	// pre-summarize snapshot, not the mutated history).
	want := []brain.Turn{
		{Role: "assistant", Content: "the summary"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "reply two"},
	}
	if len(b.history) != len(want) {
		t.Fatalf("reseeded history = %+v, want %+v", b.history, want)
	}
	for i := range want {
		if b.history[i] != want[i] {
			t.Fatalf("seed[%d] = %+v, want %+v", i, b.history[i], want[i])
		}
	}
	// Backup got the pre-compact turns, untouched by the summarize turn.
	if len(backup) != 4 || backup[3].Content != "reply two" {
		t.Fatalf("backup = %+v, want the 4 original turns", backup)
	}
	if len(compacted) != 1 || compacted[0].TurnsBefore != 4 || compacted[0].Summary != "the summary" {
		t.Fatalf("events = %+v, want one ConversationCompacted{TurnsBefore:4}", compacted)
	}
	if saves != 1 {
		t.Fatalf("OnTurn calls = %d, want 1 (persist the compacted session)", saves)
	}
}

// An ollama tool loop leaves history ending on tool/assistant turns. Seeding a
// blind last-two slice then strands a tool result — brain.Turn has no tool-call
// id, so the assistant message that requested it can never come along — and
// loses the user turn that started the exchange.
func TestCompactConversationSeedsPastToolTurns(t *testing.T) {
	toolTail := []brain.Turn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply one"},
		{Role: "user", Content: "what is in that file"},
		{Role: "assistant", Content: ""}, // tool-call-only message
		{Role: "tool", Content: "file contents"},
		{Role: "assistant", Content: "it holds the config"},
	}
	b := &compactBrain{history: toolTail, summary: "the summary"}
	p := &Pipeline{Brain: b, CompactPrompt: "S"}

	if err := p.CompactConversation(context.Background()); err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}
	assertTurns(t, b.history, []brain.Turn{
		{Role: "assistant", Content: "the summary"},
		{Role: "user", Content: "what is in that file"},
		{Role: "assistant", Content: "it holds the config"},
	}, "seed after a tool-loop tail")
}

func TestCompactTailBounds(t *testing.T) {
	// No user turn within the bound: the tail is capped instead of walking the
	// whole transcript back, so the seed still fits the ≤6-turn flatten window.
	var assistantOnly []brain.Turn
	for range 8 {
		assistantOnly = append(assistantOnly, brain.Turn{Role: "assistant", Content: "reply"})
	}
	if got := compactTail(assistantOnly); len(got) != maxCompactTail {
		t.Fatalf("tail length = %d, want %d (bounded)", len(got), maxCompactTail)
	}

	// Everything filtered out is an empty tail, not a panic: the summary alone
	// reseeds the conversation.
	if got := compactTail([]brain.Turn{{Role: "tool", Content: "x"}, {Role: "assistant", Content: " "}}); len(got) != 0 {
		t.Fatalf("tail = %+v, want empty", got)
	}
}

func TestCompactConversationGuards(t *testing.T) {
	t.Run("no prompt configured", func(t *testing.T) {
		p := &Pipeline{Brain: &compactBrain{history: compactHistory()}}
		if err := p.CompactConversation(context.Background()); err == nil {
			t.Fatal("want error when CompactPrompt is empty")
		}
	})

	t.Run("nothing to compact", func(t *testing.T) {
		p := &Pipeline{Brain: &compactBrain{}, CompactPrompt: "S"}
		if err := p.CompactConversation(context.Background()); err == nil {
			t.Fatal("want error on empty history")
		}
	})

	t.Run("summarize failure leaves history intact", func(t *testing.T) {
		b := &compactBrain{history: compactHistory(), fullErr: errors.New("model down")}
		saves := 0
		p := &Pipeline{Brain: b, CompactPrompt: "S", OnTurn: func() { saves++ }}
		err := p.CompactConversation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "model down") {
			t.Fatalf("error = %v, want wrapped summarize failure", err)
		}
		if len(b.loaded) != 0 {
			t.Fatal("LoadHistory must not run when the summarize turn fails")
		}
		if saves != 0 {
			t.Fatal("OnTurn must not run on failure")
		}
		// The conversation must be exactly what it was: no compact briefing
		// left parked as a user turn, and no reseed.
		assertTurns(t, b.history, compactHistory(), "history after a failed summarize")
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b := &compactBrain{history: compactHistory(), summary: "s"}
		p := &Pipeline{Brain: b, CompactPrompt: "S"}
		if err := p.CompactConversation(ctx); err == nil {
			t.Fatal("want error from canceled context")
		}
		if len(b.loaded) != 0 {
			t.Fatal("LoadHistory must not run after cancellation")
		}
		assertTurns(t, b.history, compactHistory(), "history after cancellation")
	})
}
