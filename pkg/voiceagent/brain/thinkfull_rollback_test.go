package brain

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// ThinkFull has to append the input before the call — the prompt is built from
// history — so a turn that never answered must roll that append back. Otherwise
// the unanswered input stays in the transcript and is re-sent as context by
// every later turn. /compact depends on this: a failed summarize turn would
// otherwise leave its briefing parked in the conversation as a user turn.

func seededHistory() []Turn {
	return []Turn{
		{Role: "user", Content: "one"},
		{Role: "samantha", Content: "reply one"},
	}
}

func assertHistory(t *testing.T, got, want []Turn, context string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: history = %+v, want %+v", context, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: history[%d] = %+v, want %+v", context, i, got[i], want[i])
		}
	}
}

func TestThinkFullRollsBackHistoryOnError(t *testing.T) {
	t.Run("run error", func(t *testing.T) {
		fake := &fakeClaudeClient{fullErrs: []error{errors.New("cli down")}}
		b := newTestBrain(fake)
		b.LoadHistory(seededHistory())

		if _, err := b.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the CLI failure")
		}
		assertHistory(t, b.History(), seededHistory(), "after failed ThinkFull")
	})

	t.Run("is_error result", func(t *testing.T) {
		fake := &fakeClaudeClient{
			fullResults: []*claude.ClaudeResult{{IsError: true, Subtype: "error_during_execution"}},
		}
		b := newTestBrain(fake)
		b.LoadHistory(seededHistory())

		if _, err := b.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the is_error result")
		}
		assertHistory(t, b.History(), seededHistory(), "after is_error result")
	})

	t.Run("both resume attempts fail", func(t *testing.T) {
		fake := &fakeClaudeClient{fullErrs: []error{errors.New("stale session"), errors.New("cli down")}}
		b := newTestBrain(fake)
		b.LoadHistory(seededHistory())
		b.sessionID = "sess-1"

		if _, err := b.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the retry failure")
		}
		// The retry needs the input in history to build its prompt, so the
		// rollback must happen after it — not before.
		if fake.runCalls != 2 {
			t.Fatalf("RunPromptCtx calls = %d, want 2 (resume failure retries once)", fake.runCalls)
		}
		assertHistory(t, b.History(), seededHistory(), "after failed retry")
	})

	t.Run("success keeps the exchange", func(t *testing.T) {
		fake := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "the summary"}}}
		b := newTestBrain(fake)
		b.LoadHistory(seededHistory())

		if _, err := b.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		assertHistory(t, b.History(), append(seededHistory(),
			Turn{Role: "user", Content: "COMPACT"},
			Turn{Role: "samantha", Content: "the summary"},
		), "after successful ThinkFull")
	})
}

func TestGrokThinkFullRollsBackHistoryOnError(t *testing.T) {
	t.Run("run error", func(t *testing.T) {
		fake := &fakeGrokClient{fullErrs: []error{errors.New("cli down")}}
		g := newTestGrokBrain(fake)
		g.LoadHistory(seededHistory())

		if _, err := g.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the CLI failure")
		}
		assertHistory(t, g.History(), seededHistory(), "after failed ThinkFull")
	})

	t.Run("is_error result", func(t *testing.T) {
		fake := &fakeGrokClient{fullResults: []*grok.GrokResult{{IsError: true, Subtype: "session_not_found"}}}
		g := newTestGrokBrain(fake)
		g.LoadHistory(seededHistory())

		if _, err := g.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the is_error result")
		}
		assertHistory(t, g.History(), seededHistory(), "after is_error result")
	})

	t.Run("success keeps the exchange", func(t *testing.T) {
		fake := &fakeGrokClient{fullResults: []*grok.GrokResult{{Text: "the summary"}}}
		g := newTestGrokBrain(fake)
		g.LoadHistory(seededHistory())

		if _, err := g.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err != nil {
			t.Fatalf("ThinkFull() error = %v", err)
		}
		assertHistory(t, g.History(), append(seededHistory(),
			Turn{Role: "user", Content: "COMPACT"},
			Turn{Role: "samantha", Content: "the summary"},
		), "after successful ThinkFull")
	})
}

// ollamaFailStub serves the chat API, optionally answering the first call with a
// tool call so the failure lands after the tool loop has already appended turns.
func ollamaFailStub(t *testing.T, toolCallFirst bool) *api.Client {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		if toolCallFirst && calls == 1 {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_skills","arguments":{}}}]},"done":true,"done_reason":"stop"}`+"\n")
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"server exploded"}`)
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestOllamaThinkFullRollsBackHistoryOnError(t *testing.T) {
	t.Run("chat error", func(t *testing.T) {
		o := &OllamaBrain{client: ollamaFailStub(t, false), model: "m", cfg: &config.Config{MaxHistory: 10}}
		o.LoadHistory(seededHistory())

		if _, err := o.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the server failure")
		}
		// Ollama stores replies as "assistant"; LoadHistory mapped the seed.
		assertHistory(t, o.History(), []Turn{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "reply one"},
		}, "after failed ThinkFull")
	})

	t.Run("failure after a tool call", func(t *testing.T) {
		o := &OllamaBrain{
			client:  ollamaFailStub(t, true),
			model:   "m",
			cfg:     &config.Config{MaxHistory: 10},
			workDir: t.TempDir(),
		}
		o.LoadHistory(seededHistory())

		if _, err := o.ThinkFull(context.Background(), "COMPACT", StreamOptions{ToolsEnabled: true}); err == nil {
			t.Fatal("ThinkFull() error = nil, want the server failure")
		}
		// The tool loop appended assistant + tool turns before failing; a
		// truncate-to-length rollback would strand them.
		assertHistory(t, o.History(), []Turn{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "reply one"},
		}, "after failure following a tool call")
	})
}

// A hostile detail of the ollama rollback: ensureContextBudget can trim the
// front of history before the call, so the pre-call snapshot — not the length —
// defines what to restore.
func TestOllamaThinkFullRollbackSurvivesContextTrim(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 4000)
	seed := []Turn{
		{Role: "user", Content: string(long)},
		{Role: "assistant", Content: string(long)},
		{Role: "user", Content: string(long)},
		{Role: "assistant", Content: string(long)},
	}
	o := &OllamaBrain{
		client: ollamaFailStub(t, false),
		model:  "m",
		// Small num_ctx forces ensureContextBudget to drop the oldest turns.
		cfg: &config.Config{MaxHistory: 10, OllamaNumCtx: 512},
	}
	o.LoadHistory(seed)

	if _, err := o.ThinkFull(context.Background(), "COMPACT", StreamOptions{}); err == nil {
		t.Fatal("ThinkFull() error = nil, want the server failure")
	}
	assertHistory(t, o.History(), seed, "after failure that followed a context trim")
}
