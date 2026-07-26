package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
)

// compactBrain is a stateful fake: ThinkFull records the instruction and
// appends the turn to history the way real providers do, so the test can
// verify the snapshot/tail semantics of CompactConversation.
type compactBrain struct {
	history     []brain.Turn
	summary     string
	fullErr     error
	thinkInputs []string
	loaded      [][]brain.Turn
}

func (b *compactBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("not used")
}

func (b *compactBrain) ThinkFull(ctx context.Context, input string, _ brain.StreamOptions) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	b.thinkInputs = append(b.thinkInputs, input)
	if b.fullErr != nil {
		return "", b.fullErr
	}
	b.history = append(b.history, brain.Turn{Role: "user", Content: input}, brain.Turn{Role: "samantha", Content: b.summary})
	return b.summary, nil
}

func (b *compactBrain) ClearHistory()         {}
func (b *compactBrain) History() []brain.Turn { return b.history }
func (b *compactBrain) LoadHistory(turns []brain.Turn) {
	b.history = turns
	b.loaded = append(b.loaded, turns)
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
	})
}
