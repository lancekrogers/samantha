package brain

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestModelRejectedTools(t *testing.T) {
	for msg, want := range map[string]bool{
		"400 Bad Request: registry.ollama.ai/library/kimi does not support tools": true,
		"model does not support tools":                                            true,
		"connection refused":                                                      false,
		"some other 400":                                                          false,
	} {
		if got := modelRejectedTools(errors.New(msg)); got != want {
			t.Errorf("modelRejectedTools(%q) = %v, want %v", msg, got, want)
		}
	}
}

// ollamaStub serves the Ollama chat API, rejecting requests that carry tools so
// we can exercise the retry-without-tools path.
func ollamaStub(t *testing.T, withTools, withoutTools *int) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools":`)) {
			*withTools++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"model does not support tools"}`)
			return
		}
		*withoutTools++
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

// ollamaStreamStub serves the Ollama chat API as NDJSON, one response line per
// entry, so streaming-delta behavior can be exercised without a real server.
func ollamaStreamStub(t *testing.T, lines []string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

// TestThinkStreamStreamsDeltasWithToolsEnabled pins the fix: with tools enabled
// but no tool call this turn, each content delta must stream through as its own
// chunk rather than being buffered and sent as one combined chunk.
func TestThinkStreamStreamsDeltasWithToolsEnabled(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"lo"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":" world."},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}

	var got []string
	for c := range stream.Chunks {
		got = append(got, c)
	}
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}

	want := []string{"Hel", "lo", " world."}
	if len(got) != len(want) {
		t.Fatalf("streamed chunks = %v, want %v (tools-enabled path must stream, not buffer)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Tool-only turns used to close the stream with zero chunks, so the TUI showed
// tool activity and then nothing — "looking into it" with no reply.
func TestThinkStreamFallbackAfterToolOnlyTurn(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			// First response: tool call, no text.
			line := `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`
			_, _ = io.WriteString(w, line+"\n")
			return
		}
		// Second response after tool results: empty content again.
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}

	var chunks []string
	for c := range stream.Chunks {
		chunks = append(chunks, c)
	}
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if reqs < 2 {
		t.Fatalf("expected tool loop to re-request model, got %d chat requests", reqs)
	}
	joined := strings.Join(chunks, "")
	if joined != fallbackResponse {
		t.Fatalf("streamed %q, want fallback %q after empty tool-only turn", joined, fallbackResponse)
	}
	if len(o.history) == 0 || o.history[len(o.history)-1].Content != fallbackResponse {
		t.Fatalf("history tail = %+v, want fallback assistant message", o.history)
	}
}

func TestChatRetriesWithoutToolsWhenUnsupported(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{client: ollamaStub(t, &withTools, &withoutTools), model: "m"}

	stream := false
	req := &api.ChatRequest{
		Model:    "m",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Tools:    voiceAssistantTools(nil),
		Stream:   &stream,
	}

	var got api.Message
	if err := o.chat(context.Background(), req, func(resp api.ChatResponse) error {
		got = resp.Message
		return nil
	}); err != nil {
		t.Fatalf("chat returned error: %v", err)
	}

	if got.Content != "hi" {
		t.Errorf("content = %q, want %q", got.Content, "hi")
	}
	if withTools != 1 || withoutTools != 1 {
		t.Errorf("want 1 tool-call then 1 retry without; got withTools=%d withoutTools=%d", withTools, withoutTools)
	}
	if req.Tools != nil {
		t.Error("tools should be cleared after the unsupported-tools retry")
	}
}

// recordingStub captures each request body so a test can assert what was sent.
func recordingStub(t *testing.T, count *int, bodies *[][]byte) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*count++
		body, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, body)
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestWarmupFiresMinimalRequest(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m"}

	o.Warmup(context.Background())

	if count != 1 {
		t.Fatalf("want exactly 1 warmup request, got %d", count)
	}
	body := bodies[0]
	if bytes.Contains(body, []byte(`"tools":`)) {
		t.Errorf("warmup must not send tools; body=%s", body)
	}
	if !bytes.Contains(body, []byte(`"num_predict":1`)) {
		t.Errorf("warmup must cap generation with num_predict; body=%s", body)
	}
}

// TestSystemPrefixStableAcrossTurns guards the KV-cache reuse property: the
// system message (the cached prefix) must be byte-identical across turns, even
// as conversation history grows.
func TestSystemPrefixStableAcrossTurns(t *testing.T) {
	o := &OllamaBrain{
		workDir: "/work/dir",
		cfg:     &config.Config{AgentName: "Samantha", MaxHistory: 10},
	}

	o.history = append(o.history, api.Message{Role: "user", Content: "first"})
	firstPrefix := o.buildMessages()[0].Content

	o.history = append(o.history,
		api.Message{Role: "assistant", Content: "hello there"},
		api.Message{Role: "user", Content: "second"},
	)
	secondPrefix := o.buildMessages()[0].Content

	if firstPrefix != secondPrefix {
		t.Errorf("system prefix changed across turns, defeating KV-cache reuse:\nfirst=%q\nsecond=%q", firstPrefix, secondPrefix)
	}
}

// toolGroupHistory is a two-turn conversation where each turn contains a
// tool-call group: user, assistant{ToolCalls}, tool results, final assistant.
func toolGroupHistory() []api.Message {
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{}}}
	return []api.Message{
		{Role: "user", Content: "u1"},      // 0
		call,                               // 1
		{Role: "tool", Content: "r1"},      // 2
		{Role: "tool", Content: "r2"},      // 3
		{Role: "assistant", Content: "a1"}, // 4
		{Role: "user", Content: "u2"},      // 5
		call,                               // 6
		{Role: "tool", Content: "r3"},      // 7
		{Role: "assistant", Content: "a2"}, // 8
	}
}

// assertNoStrandedTools fails if msgs starts with a tool result or contains a
// tool result whose assistant tool-call antecedent was trimmed away.
func assertNoStrandedTools(t *testing.T, msgs []api.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if i == 0 {
			t.Fatal("message window starts with role tool")
		}
		prev := msgs[i-1]
		if prev.Role == "tool" || (prev.Role == "assistant" && len(prev.ToolCalls) > 0) {
			continue
		}
		t.Fatalf("tool result at index %d lacks its assistant tool-call antecedent", i)
	}
}

func TestHistoryWindowStart(t *testing.T) {
	hist := toolGroupHistory()

	tests := []struct {
		name string
		max  int
		want int
	}{
		{"window opens on tool, no later user, drops tail group", 2, 9},
		{"window opens on tool result, advances to next user", 7, 5},
		{"window opens mid first tool group", 6, 5},
		{"window opens on user", 4, 5},
		{"window opens on assistant tool-call", 3, 6},
		{"window fits whole history", 9, 0},
		{"max larger than history", 20, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := historyWindowStart(hist, tt.max)
			if got != tt.want {
				t.Fatalf("historyWindowStart(max=%d) = %d, want %d", tt.max, got, tt.want)
			}
			assertNoStrandedTools(t, hist[got:])
		})
	}
}

func TestBuildMessagesNeverStrandsToolResults(t *testing.T) {
	for maxHistory := 1; maxHistory <= 5; maxHistory++ {
		o := &OllamaBrain{
			workDir: "/work/dir",
			cfg:     &config.Config{AgentName: "Samantha", MaxHistory: maxHistory},
			history: toolGroupHistory(),
		}
		msgs := o.buildMessages()
		if msgs[0].Role != "system" {
			t.Fatalf("maxHistory=%d: first message role = %q, want system", maxHistory, msgs[0].Role)
		}
		assertNoStrandedTools(t, msgs[1:])
	}
}

func TestBuildMessagesWithNonPositiveHistoryWindow(t *testing.T) {
	for _, maxHistory := range []int{0, -1} {
		o := &OllamaBrain{
			workDir: "/work/dir",
			cfg:     &config.Config{AgentName: "Samantha", MaxHistory: maxHistory},
			history: toolGroupHistory(),
		}
		msgs := o.buildMessages()
		if len(msgs) != 1 {
			t.Fatalf("maxHistory=%d: got %d messages, want system prompt only", maxHistory, len(msgs))
		}
		if msgs[0].Role != "system" {
			t.Fatalf("maxHistory=%d: first message role = %q, want system", maxHistory, msgs[0].Role)
		}
	}
}

func TestTrimHistoryNeverStrandsToolResults(t *testing.T) {
	o := &OllamaBrain{
		cfg:     &config.Config{MaxHistory: 3},
		history: toolGroupHistory(),
	}
	o.trimHistory()
	if len(o.history) == 0 {
		t.Fatal("trimHistory dropped all history")
	}
	if o.history[0].Role == "tool" {
		t.Fatal("trimmed history starts with role tool")
	}
	assertNoStrandedTools(t, o.history)
}

func TestTrimHistoryWithNonPositiveHistoryWindow(t *testing.T) {
	for _, maxHistory := range []int{0, -1} {
		o := &OllamaBrain{
			cfg:     &config.Config{MaxHistory: maxHistory},
			history: toolGroupHistory(),
		}
		o.trimHistory()
		if len(o.history) != 0 {
			t.Fatalf("maxHistory=%d: got %d history messages, want none", maxHistory, len(o.history))
		}
	}
}

// TestThinkFullSpeaksFallbackVerbatim: an empty model response must yield the
// canned fallback untouched — cleanForVoice runs before the fallback is
// substituted, never on it.
func TestThinkFullSpeaksFallbackVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp != fallbackResponse {
		t.Errorf("resp = %q, want fallback verbatim %q", resp, fallbackResponse)
	}
}

func TestThinkFullOmitsToolsWhenDisabled(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{
		client:  ollamaStub(t, &withTools, &withoutTools),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello", StreamOptions{ToolsEnabled: false})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp == "" {
		t.Fatal("expected a response")
	}
	if withTools != 0 {
		t.Errorf("tools-disabled config must not send tools; got %d tool requests", withTools)
	}
}

func TestThinkFullSendsToolsWhenEnabled(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{
		client:  ollamaStub(t, &withTools, &withoutTools),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	// Stub rejects tools and retries without — withTools must still be > 0
	// so we know ToolsEnabled actually attached the schema.
	_, err := o.ThinkFull(context.Background(), "hello", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if withTools == 0 {
		t.Fatal("ToolsEnabled=true must send a tools request")
	}
}

// ollamaErrorStub serves the chat API with a hard 500 so error-path recovery
// can be exercised without a real server.
func ollamaErrorStub(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestThinkStreamChatErrorStreamsRecoveryReply(t *testing.T) {
	// The recovery invariant (WI-c8884d P1): a hard chat error must not end
	// the stream silently — the recovery line streams to the caller, lands in
	// history for the next turn, and the error surfaces as Recovered detail.
	o := &OllamaBrain{client: ollamaErrorStub(t), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "do something hard", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	var got []string
	for c := range stream.Chunks {
		got = append(got, c)
	}
	res := <-stream.Done
	if res.Err == nil || !res.Recovered {
		t.Fatalf("stream done = %+v, want a Recovered error result", res)
	}
	if joined := strings.Join(got, ""); !strings.Contains(joined, RecoveryReply) {
		t.Fatalf("streamed chunks = %q, want recovery reply", joined)
	}
	hist := o.History()
	if len(hist) == 0 || hist[len(hist)-1].Content != RecoveryReply {
		t.Fatalf("history tail = %+v, want assistant recovery reply recorded", hist)
	}
}

func TestThinkStreamCanceledCtxSkipsRecovery(t *testing.T) {
	// Shutdown and barge-in cancel the turn context; no one is listening for
	// a recovery line, so the raw error must pass through un-recovered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := &OllamaBrain{client: ollamaErrorStub(t), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(ctx, "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	for range stream.Chunks {
	}
	res := <-stream.Done
	if res.Err == nil || res.Recovered {
		t.Fatalf("stream done = %+v, want raw error without recovery on canceled ctx", res)
	}
}
