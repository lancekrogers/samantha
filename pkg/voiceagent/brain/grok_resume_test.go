package brain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

type grokStreamScript struct {
	events []grok.Event
	err    error
}

// fakeGrokClient scripts stream/blocking turns and records the prompts and
// options each attempt used, mirroring fakeClaudeClient.
type fakeGrokClient struct {
	streamScripts []grokStreamScript
	fullResults   []*grok.GrokResult
	fullErrs      []error

	streamPrompts []string
	streamOpts    []*grok.RunOptions
	runPrompts    []string
	runOpts       []*grok.RunOptions
}

func (f *fakeGrokClient) StreamPrompt(_ context.Context, prompt string, opts *grok.RunOptions) (<-chan grok.Event, <-chan error) {
	f.streamPrompts = append(f.streamPrompts, prompt)
	f.streamOpts = append(f.streamOpts, opts)

	script := grokStreamScript{}
	if len(f.streamScripts) > 0 {
		script = f.streamScripts[0]
		f.streamScripts = f.streamScripts[1:]
	}

	events := make(chan grok.Event, len(script.events))
	errs := make(chan error, 1)
	for _, ev := range script.events {
		events <- ev
	}
	if script.err != nil {
		errs <- script.err
	}
	close(events)
	close(errs)
	return events, errs
}

func (f *fakeGrokClient) RunPromptCtx(_ context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error) {
	f.runPrompts = append(f.runPrompts, prompt)
	f.runOpts = append(f.runOpts, opts)

	i := len(f.runPrompts) - 1
	var err error
	if i < len(f.fullErrs) {
		err = f.fullErrs[i]
	}
	if err != nil {
		return nil, err
	}
	if i < len(f.fullResults) {
		return f.fullResults[i], nil
	}
	return &grok.GrokResult{Text: "ok"}, nil
}

func newTestGrokBrain(c grokRunner) *GrokBrain {
	return &GrokBrain{
		client:          c,
		cfg:             &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt:    "SYSTEM",
		turnInstruction: "Reply now.",
	}
}

func grokTextEvent(text, sessionID string) grok.Event {
	return grok.Event{Type: grok.EventText, Text: text, SessionID: sessionID}
}

func mustGrokStream(t *testing.T, g *GrokBrain, input string) *Stream {
	t.Helper()
	s, err := g.ThinkStream(context.Background(), input, StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream(%q) error = %v", input, err)
	}
	return s
}

func TestGrokStreamCapturesSessionAndResumes(t *testing.T) {
	fake := &fakeGrokClient{
		streamScripts: []grokStreamScript{
			{events: []grok.Event{grokTextEvent("One.", "gsess-1"), {Type: grok.EventEnd, SessionID: "gsess-1"}}},
			{events: []grok.Event{grokTextEvent("Two.", "gsess-1")}},
		},
	}
	g := newTestGrokBrain(fake)

	collectStream(t, mustGrokStream(t, g, "first"))
	collectStream(t, mustGrokStream(t, g, "second"))

	if got := fake.streamOpts[0].ResumeID; got != "" {
		t.Fatalf("turn 1 ResumeID = %q, want empty on the first turn", got)
	}
	if got := fake.streamOpts[1].ResumeID; got != "gsess-1" {
		t.Fatalf("turn 2 ResumeID = %q, want gsess-1", got)
	}
	// With a live session the prompt must carry only the new turn.
	if p := fake.streamPrompts[1]; strings.Contains(p, "Recent conversation:") || !strings.Contains(p, "User: second") {
		t.Fatalf("turn 2 prompt must be new-text-only:\n%s", p)
	}
}

func TestGrokStreamFlattensOnlyWithoutSession(t *testing.T) {
	fake := &fakeGrokClient{
		streamScripts: []grokStreamScript{
			// No session id ever arrives: every turn stays on the flatten path.
			{events: []grok.Event{grokTextEvent("One.", "")}},
			{events: []grok.Event{grokTextEvent("Two.", "")}},
		},
	}
	g := newTestGrokBrain(fake)

	collectStream(t, mustGrokStream(t, g, "first"))
	collectStream(t, mustGrokStream(t, g, "second"))

	if p := fake.streamPrompts[1]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("turn 2 without a session must flatten history:\n%s", p)
	}
}

func TestGrokStreamRetriesOnceWithoutSessionOnRejectedResume(t *testing.T) {
	fake := &fakeGrokClient{
		streamScripts: []grokStreamScript{
			{events: []grok.Event{grokTextEvent("One.", "gsess-1")}},
			// Resumed turn dies before any text (stale session file).
			{err: errors.New("session not found")},
			// Retry without the id succeeds and captures a fresh session.
			{events: []grok.Event{grokTextEvent("Two.", "gsess-2")}},
		},
	}
	g := newTestGrokBrain(fake)

	collectStream(t, mustGrokStream(t, g, "first"))
	if _, res := collectStream(t, mustGrokStream(t, g, "second")); res.Err != nil {
		t.Fatalf("turn 2 should recover via retry, got %v", res.Err)
	}

	if got := fake.streamOpts[1].ResumeID; got != "gsess-1" {
		t.Fatalf("attempt 2 ResumeID = %q, want gsess-1", got)
	}
	if got := fake.streamOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty after dropping the session", got)
	}
	if p := fake.streamPrompts[2]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("retry must re-flatten history:\n%s", p)
	}
	if g.sessionID != "gsess-2" {
		t.Fatalf("sessionID = %q, want the fresh session captured on retry", g.sessionID)
	}
}

func TestGrokStreamErrorEventTriggersRetry(t *testing.T) {
	fake := &fakeGrokClient{
		streamScripts: []grokStreamScript{
			{events: []grok.Event{grokTextEvent("One.", "gsess-1")}},
			// Exit-0 failure shape: an error event and nothing else.
			{events: []grok.Event{{Type: grok.EventError, Message: "cannot resume"}}},
			{events: []grok.Event{grokTextEvent("Two.", "gsess-2")}},
		},
	}
	g := newTestGrokBrain(fake)

	collectStream(t, mustGrokStream(t, g, "first"))
	if _, res := collectStream(t, mustGrokStream(t, g, "second")); res.Err != nil {
		t.Fatalf("turn 2 should recover via retry, got %v", res.Err)
	}
	if got := fake.streamOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty", got)
	}
}

func TestGrokThinkFullCapturesSessionAndRetries(t *testing.T) {
	fake := &fakeGrokClient{
		fullResults: []*grok.GrokResult{
			{Text: "One.", SessionID: "gsess-1"},
			{Text: "won't be used", IsError: true, Subtype: "session_error"},
			{Text: "Two.", SessionID: "gsess-2"},
		},
	}
	g := newTestGrokBrain(fake)

	if _, err := g.ThinkFull(context.Background(), "first", StreamOptions{}); err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}
	if g.sessionID != "gsess-1" {
		t.Fatalf("sessionID = %q, want gsess-1 captured", g.sessionID)
	}

	// Turn 2's resumed attempt reports an exit-0 error; the retry must run
	// without the session and succeed.
	if _, err := g.ThinkFull(context.Background(), "second", StreamOptions{}); err != nil {
		t.Fatalf("turn 2 should recover via retry, got %v", err)
	}
	if got := fake.runOpts[1].ResumeID; got != "gsess-1" {
		t.Fatalf("attempt 2 ResumeID = %q, want gsess-1", got)
	}
	if got := fake.runOpts[2].ResumeID; got != "" {
		t.Fatalf("retry ResumeID = %q, want empty", got)
	}
	if g.sessionID != "gsess-2" {
		t.Fatalf("sessionID = %q, want gsess-2 after retry", g.sessionID)
	}

	// Every blocking attempt must request JSON output: the SDK's plain-text
	// decode returns Text alone, which would silently disable session capture
	// and the IsError guard against a real CLI.
	for i, opts := range fake.runOpts {
		if opts.Format != grok.JSONOutput {
			t.Fatalf("attempt %d Format = %q, want %q", i, opts.Format, grok.JSONOutput)
		}
	}
	// A resumed blocking turn sends only the new text, no flatten.
	if p := fake.runPrompts[1]; strings.Contains(p, "Recent conversation:") || !strings.Contains(p, "User: second") {
		t.Fatalf("resumed blocking prompt must be new-text-only:\n%s", p)
	}
}

func TestGrokDropsSessionOnClearAndLoadHistory(t *testing.T) {
	g := newTestGrokBrain(&fakeGrokClient{})
	g.sessionID = "gsess-1"

	g.ClearHistory()
	if g.sessionID != "" {
		t.Fatalf("ClearHistory: sessionID = %q, want empty", g.sessionID)
	}

	g.sessionID = "gsess-2"
	g.LoadHistory([]Turn{{Role: "user", Content: "hi"}})
	if g.sessionID != "" {
		t.Fatalf("LoadHistory: sessionID = %q, want empty", g.sessionID)
	}
	if len(g.history) != 1 {
		t.Fatalf("LoadHistory history len = %d, want 1", len(g.history))
	}
}

func TestGrokSessionInfoReportsLiveSession(t *testing.T) {
	g := newTestGrokBrain(&fakeGrokClient{})
	g.sessionID = "gsess-12345678"
	g.history = []Turn{{Role: "user", Content: "hi"}}

	want := SessionState{Kind: "harness", ID: "gsess-12345678", Turns: 1}
	if got := g.SessionInfo(); got != want {
		t.Fatalf("SessionInfo() = %+v, want %+v", got, want)
	}
}
