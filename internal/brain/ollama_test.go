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

func TestChatRetriesWithoutToolsWhenUnsupported(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{client: ollamaStub(t, &withTools, &withoutTools), model: "m"}

	stream := false
	req := &api.ChatRequest{
		Model:    "m",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Tools:    voiceAssistantTools(),
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

func TestThinkFullOmitsToolsWhenDisabled(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{
		client:  ollamaStub(t, &withTools, &withoutTools),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{VoiceToolsEnabled: false, MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello")
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
