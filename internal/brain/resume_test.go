package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/lancekrogers/samantha/internal/config"
)

// streamScript is one scripted StreamPrompt call: the messages it emits and an
// optional terminal error (mirroring the SDK's message/error channel pair).
type streamScript struct {
	msgs []claude.Message
	err  error
}

// fakeClaudeClient is the in-package fake claudeRunner for Brain resume tests.
// It records every prompt/opts it is called with and replays scripted results
// by call index, so tests assert what reached the CLI without shelling out.
type fakeClaudeClient struct {
	streamScripts []streamScript
	fullResults   []*claude.ClaudeResult
	fullErrs      []error

	streamPrompts []string
	streamOpts    []*claude.RunOptions
	streamCalls   int

	runPrompts []string
	runOpts    []*claude.RunOptions
	runCalls   int
}

var _ claudeRunner = (*fakeClaudeClient)(nil)

func (f *fakeClaudeClient) StreamPrompt(_ context.Context, prompt string, opts *claude.RunOptions) (<-chan claude.Message, <-chan error) {
	i := f.streamCalls
	f.streamCalls++
	f.streamPrompts = append(f.streamPrompts, prompt)
	f.streamOpts = append(f.streamOpts, opts)

	var sc streamScript
	if i < len(f.streamScripts) {
		sc = f.streamScripts[i]
	}
	msgCh := make(chan claude.Message, len(sc.msgs))
	errCh := make(chan error, 1)
	for _, m := range sc.msgs {
		msgCh <- m
	}
	close(msgCh)
	if sc.err != nil {
		errCh <- sc.err
	}
	close(errCh)
	return msgCh, errCh
}

func (f *fakeClaudeClient) RunPromptCtx(_ context.Context, prompt string, opts *claude.RunOptions) (*claude.ClaudeResult, error) {
	i := f.runCalls
	f.runCalls++
	f.runPrompts = append(f.runPrompts, prompt)
	f.runOpts = append(f.runOpts, opts)

	if i < len(f.fullErrs) && f.fullErrs[i] != nil {
		return nil, f.fullErrs[i]
	}
	if i < len(f.fullResults) {
		return f.fullResults[i], nil
	}
	return &claude.ClaudeResult{}, nil
}

func assistantMsg(text string) claude.Message {
	body, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	})
	return claude.Message{Type: "assistant", Message: body}
}

func resultMsg(session string) claude.Message {
	return claude.Message{Type: "result", SessionID: session}
}

func newTestBrain(c claudeRunner) *Brain {
	return &Brain{
		client:          c,
		cfg:             &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt:    "SYSTEM",
		turnInstruction: "Reply now.",
	}
}

func collectStream(t *testing.T, s *Stream) (string, StreamResult) {
	t.Helper()
	var sb strings.Builder
	for c := range s.Chunks {
		sb.WriteString(c)
	}
	return sb.String(), <-s.Done
}

func TestThinkStreamCapturesSessionID(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("Hi there."), resultMsg("sess-1")}},
		},
	}
	b := newTestBrain(fake)

	s, err := b.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	text, res := collectStream(t, s)
	if res.Err != nil {
		t.Fatalf("stream result error = %v", res.Err)
	}
	if text != "Hi there." {
		t.Fatalf("text = %q, want %q", text, "Hi there.")
	}
	if b.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want %q", b.sessionID, "sess-1")
	}
	// First turn must not resume.
	if got := fake.streamOpts[0].ResumeID; got != "" {
		t.Fatalf("first-turn ResumeID = %q, want empty", got)
	}
}

func TestThinkStreamSecondTurnResumesWithoutHistory(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("First reply."), resultMsg("sess-1")}},
			{msgs: []claude.Message{assistantMsg("Second reply."), resultMsg("sess-1")}},
		},
	}
	b := newTestBrain(fake)

	_, r1 := collectStream(t, mustStream(t, b, "first question"))
	if r1.Err != nil {
		t.Fatalf("turn 1 error = %v", r1.Err)
	}
	_, r2 := collectStream(t, mustStream(t, b, "second question"))
	if r2.Err != nil {
		t.Fatalf("turn 2 error = %v", r2.Err)
	}

	second := fake.streamPrompts[1]
	if strings.Contains(second, "Recent conversation:") {
		t.Fatalf("resumed turn re-sent flattened history:\n%s", second)
	}
	if !strings.Contains(second, "second question") {
		t.Fatalf("resumed prompt missing new input:\n%s", second)
	}
	if got := fake.streamOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("second-turn ResumeID = %q, want %q", got, "sess-1")
	}
}

func TestClearHistoryDropsSessionID(t *testing.T) {
	b := newTestBrain(&fakeClaudeClient{})
	b.sessionID = "sess-9"
	b.history = []Turn{{Role: "user", Content: "x"}}

	b.ClearHistory()

	if b.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty after ClearHistory", b.sessionID)
	}
	if b.history != nil {
		t.Fatalf("history = %+v, want nil after ClearHistory", b.history)
	}
}

func TestLoadHistoryDropsSessionID(t *testing.T) {
	b := newTestBrain(&fakeClaudeClient{})
	b.sessionID = "sess-9"

	b.LoadHistory([]Turn{{Role: "user", Content: "x"}})

	if b.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty after LoadHistory", b.sessionID)
	}
}

func TestThinkStreamResumeFailureFallsBackToFlatten(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			// turn 1: succeeds, captures sess-1.
			{msgs: []claude.Message{assistantMsg("First reply."), resultMsg("sess-1")}},
			// turn 2 attempt A (resume): errors before any text.
			{err: errors.New("no conversation found for session sess-1")},
			// turn 2 attempt B (flatten retry): succeeds, captures sess-2.
			{msgs: []claude.Message{assistantMsg("Recovered reply."), resultMsg("sess-2")}},
		},
	}
	b := newTestBrain(fake)

	collectStream(t, mustStream(t, b, "first question"))
	text, res := collectStream(t, mustStream(t, b, "second question"))
	if res.Err != nil {
		t.Fatalf("fallback should recover the turn, got error = %v", res.Err)
	}
	if fake.streamCalls != 3 {
		t.Fatalf("streamCalls = %d, want 3 (turn1 + resume + retry)", fake.streamCalls)
	}
	// Attempt A carried the stale resume id...
	if got := fake.streamOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("resume attempt ResumeID = %q, want %q", got, "sess-1")
	}
	// ...the retry dropped it and re-sent flattened history.
	if got := fake.streamOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty", got)
	}
	if p := fake.streamPrompts[2]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("retry prompt should flatten history:\n%s", p)
	}
	if p := fake.streamPrompts[2]; !strings.Contains(p, "second question") {
		t.Fatalf("retry prompt missing new input:\n%s", p)
	}
	if text != "Recovered reply." {
		t.Fatalf("text = %q, want %q", text, "Recovered reply.")
	}
	if b.sessionID != "sess-2" {
		t.Fatalf("sessionID = %q, want %q after retry", b.sessionID, "sess-2")
	}
}

func TestThinkFullCapturesAndResumes(t *testing.T) {
	fake := &fakeClaudeClient{
		fullResults: []*claude.ClaudeResult{
			{Result: "First reply.", SessionID: "sess-1"},
			{Result: "Second reply.", SessionID: "sess-1"},
		},
	}
	b := newTestBrain(fake)

	if _, err := b.ThinkFull(context.Background(), "first question", StreamOptions{}); err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}
	if b.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want %q", b.sessionID, "sess-1")
	}
	// JSON output is required to surface the session id.
	if f := fake.runOpts[0].Format; f != claude.JSONOutput {
		t.Fatalf("format = %q, want %q", f, claude.JSONOutput)
	}

	if _, err := b.ThinkFull(context.Background(), "second question", StreamOptions{}); err != nil {
		t.Fatalf("turn 2 error = %v", err)
	}
	second := fake.runPrompts[1]
	if strings.Contains(second, "Recent conversation:") {
		t.Fatalf("resumed turn re-sent flattened history:\n%s", second)
	}
	if !strings.Contains(second, "second question") {
		t.Fatalf("resumed prompt missing new input:\n%s", second)
	}
	if got := fake.runOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("second-turn ResumeID = %q, want %q", got, "sess-1")
	}
}

func TestThinkFullResumeFailureFallsBackToFlatten(t *testing.T) {
	fake := &fakeClaudeClient{
		fullResults: []*claude.ClaudeResult{
			{Result: "First reply.", SessionID: "sess-1"},
			nil, // attempt A errors
			{Result: "Recovered.", SessionID: "sess-2"},
		},
		fullErrs: []error{nil, errors.New("no conversation found"), nil},
	}
	b := newTestBrain(fake)

	if _, err := b.ThinkFull(context.Background(), "first question", StreamOptions{}); err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}
	out, err := b.ThinkFull(context.Background(), "second question", StreamOptions{})
	if err != nil {
		t.Fatalf("fallback should recover the turn, got error = %v", err)
	}
	if fake.runCalls != 3 {
		t.Fatalf("runCalls = %d, want 3 (turn1 + resume + retry)", fake.runCalls)
	}
	if got := fake.runOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("resume attempt ResumeID = %q, want %q", got, "sess-1")
	}
	if got := fake.runOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty", got)
	}
	if p := fake.runPrompts[2]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("retry prompt should flatten history:\n%s", p)
	}
	if out != "Recovered." {
		t.Fatalf("out = %q, want %q", out, "Recovered.")
	}
	if b.sessionID != "sess-2" {
		t.Fatalf("sessionID = %q, want %q after retry", b.sessionID, "sess-2")
	}
}

func mustStream(t *testing.T, b *Brain, input string) *Stream {
	t.Helper()
	s, err := b.ThinkStream(context.Background(), input, StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream(%q) error = %v", input, err)
	}
	return s
}

// A result message with is_error must not be spoken as the reply, and while a
// session is live it must reach the flatten retry the same way a stale --resume
// exit-1 does. The claude CLI reports some failures with exit 0 + is_error.
func TestThinkStreamResultErrorIsNotSpoken(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{{
				Type:    "result",
				Subtype: "error_during_execution",
				IsError: true,
				Result:  "Claude Code process exited with code 1",
			}}},
		},
	}
	b := newTestBrain(fake)

	text, res := collectStream(t, mustStream(t, b, "hello"))
	if res.Err == nil {
		t.Fatal("is_error result should surface as an error, not a reply")
	}
	if strings.Contains(text, "exited with code 1") {
		t.Fatalf("CLI error text reached the speaker: %q", text)
	}
}

func TestThinkStreamResultErrorWhileResumingRetriesFlattened(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("First reply."), resultMsg("sess-1")}},
			// Resume attempt: exit 0, is_error result (no text).
			{msgs: []claude.Message{{Type: "result", Subtype: "error_during_execution", IsError: true, SessionID: "sess-1", Result: "no conversation found"}}},
			{msgs: []claude.Message{assistantMsg("Recovered reply."), resultMsg("sess-2")}},
		},
	}
	b := newTestBrain(fake)

	collectStream(t, mustStream(t, b, "first question"))
	text, res := collectStream(t, mustStream(t, b, "second question"))
	if res.Err != nil {
		t.Fatalf("is_error on resume should fall back, got %v", res.Err)
	}
	if fake.streamCalls != 3 {
		t.Fatalf("streamCalls = %d, want 3", fake.streamCalls)
	}
	if got := fake.streamOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty", got)
	}
	if text != "Recovered reply." {
		t.Fatalf("text = %q", text)
	}
}

func TestThinkFullResultErrorIsNotSpoken(t *testing.T) {
	fake := &fakeClaudeClient{
		fullResults: []*claude.ClaudeResult{
			{Result: "Claude Code process exited with code 1", IsError: true, Subtype: "error_during_execution"},
		},
	}
	b := newTestBrain(fake)

	out, err := b.ThinkFull(context.Background(), "hello", StreamOptions{})
	if err == nil {
		t.Fatal("is_error result should surface as an error")
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if len(b.history) != 1 {
		t.Fatalf("history = %+v, want only the user turn", b.history)
	}
}

// assistantMsgUsage is assistantMsg plus the per-request token accounting the
// CLI stamps on assistant messages.
func assistantMsgUsage(text string, input, cacheRead, cacheCreate, output int) claude.Message {
	body, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"usage": map[string]int{
			"input_tokens":                input,
			"cache_read_input_tokens":     cacheRead,
			"cache_creation_input_tokens": cacheCreate,
			"output_tokens":               output,
		},
	})
	return claude.Message{Type: "assistant", Message: body}
}

func TestThinkStreamReportsUsage(t *testing.T) {
	// Regression: the claude path never called OnUsage, so claude users got no
	// token line in the TUI while ollama users did.
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsgUsage("Hi.", 2, 15672, 21, 7), resultMsg("sess-1")}},
		},
	}
	b := newTestBrain(fake)

	var gotPrefill, gotGen int
	var calls int
	s, err := b.ThinkStream(context.Background(), "hello", StreamOptions{
		OnUsage: func(prefill, gen int) { calls++; gotPrefill, gotGen = prefill, gen },
	})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	if _, res := collectStream(t, s); res.Err != nil {
		t.Fatalf("stream error = %v", res.Err)
	}
	if calls != 1 {
		t.Fatalf("OnUsage called %d times, want 1", calls)
	}
	// Prompt size is the sum of the three input buckets, whatever the cache served.
	if gotPrefill != 15695 {
		t.Fatalf("prefill = %d, want 15695 (2 + 15672 + 21)", gotPrefill)
	}
	if gotGen != 7 {
		t.Fatalf("gen = %d, want 7", gotGen)
	}
}

func TestSessionBudgetResetsSessionAndReflattens(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			// Turn 1 stays under budget.
			{msgs: []claude.Message{assistantMsgUsage("One.", 10, 100, 0, 5), resultMsg("sess-1")}},
			// Turn 2 crosses it — resumed, but the session is dropped afterwards.
			{msgs: []claude.Message{assistantMsgUsage("Two.", 10, 6000, 0, 5), resultMsg("sess-1")}},
			// Turn 3 must start fresh: no ResumeID, history flattened back in.
			{msgs: []claude.Message{assistantMsgUsage("Three.", 10, 100, 0, 5), resultMsg("sess-2")}},
		},
	}
	b := newTestBrain(fake)
	b.cfg.ClaudeMaxSessionTokens = 5000

	for _, q := range []string{"first", "second", "third"} {
		if _, res := collectStream(t, mustStream(t, b, q)); res.Err != nil {
			t.Fatalf("turn %q error = %v", q, res.Err)
		}
	}

	if got := fake.streamOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("turn 2 ResumeID = %q, want sess-1 (budget applies after the turn)", got)
	}
	if got := fake.streamOpts[2].ResumeID; got != "" {
		t.Fatalf("turn 3 ResumeID = %q, want empty after the budget reset", got)
	}
	if p := fake.streamPrompts[2]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("turn 3 must re-flatten history:\n%s", p)
	}
	// The reset changes the prefix, never the conversation.
	if len(b.History()) != 6 {
		t.Fatalf("history = %d turns, want 6 preserved across the reset", len(b.History()))
	}
	if b.sessionID != "sess-2" {
		t.Fatalf("sessionID = %q, want the fresh session captured", b.sessionID)
	}
}

func TestSessionBudgetDisabledResumesForever(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsgUsage("One.", 10, 999999, 0, 5), resultMsg("sess-1")}},
			{msgs: []claude.Message{assistantMsgUsage("Two.", 10, 999999, 0, 5), resultMsg("sess-1")}},
		},
	}
	b := newTestBrain(fake)
	b.cfg.ClaudeMaxSessionTokens = 0 // disabled

	collectStream(t, mustStream(t, b, "first"))
	collectStream(t, mustStream(t, b, "second"))

	if got := fake.streamOpts[1].ResumeID; got != "sess-1" {
		t.Fatalf("turn 2 ResumeID = %q, want sess-1 with the cap disabled", got)
	}
}
