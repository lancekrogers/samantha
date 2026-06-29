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
