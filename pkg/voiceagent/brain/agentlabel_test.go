package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func TestStripAgentLabel(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		in    string
		want  string
	}{
		{name: "plain label", agent: "Samantha", in: "Samantha: Hello there.", want: "Hello there."},
		{name: "lowercase label", agent: "Samantha", in: "samantha: hey!", want: "hey!"},
		{name: "bold label", agent: "Samantha", in: "**Samantha**: Hi.", want: "Hi."},
		{name: "italic label", agent: "Samantha", in: "*Samantha*: Hi.", want: "Hi."},
		{name: "underscore label", agent: "Samantha", in: "_Samantha_: Hi.", want: "Hi."},
		{name: "space before colon", agent: "Samantha", in: "Samantha : Hi.", want: "Hi."},
		{name: "leading whitespace", agent: "Samantha", in: "  \nSamantha: Hi.", want: "Hi."},
		{name: "custom agent name", agent: "Nova", in: "Nova: ready.", want: "ready."},
		{name: "empty name defaults to Samantha", agent: "", in: "Samantha: Hi.", want: "Hi."},
		{name: "label only", agent: "Samantha", in: "Samantha: ", want: ""},
		{name: "only first line stripped", agent: "Samantha", in: "Samantha: one.\nSamantha: two.", want: "one.\nSamantha: two."},
		{name: "mid-text name untouched", agent: "Samantha", in: "Ask Samantha: she knows.", want: "Ask Samantha: she knows."},
		{name: "name without colon untouched", agent: "Samantha", in: "Samantha is my name.", want: "Samantha is my name."},
		{name: "longer word sharing prefix untouched", agent: "Sam", in: "Samantha: no.", want: "Samantha: no."},
		{name: "time untouched", agent: "Samantha", in: "10:30 works for me.", want: "10:30 works for me."},
		{name: "no label untouched", agent: "Samantha", in: "Sure, sounds good.", want: "Sure, sounds good."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripAgentLabel(tt.agent, tt.in); got != tt.want {
				t.Fatalf("StripAgentLabel(%q, %q) = %q, want %q", tt.agent, tt.in, got, tt.want)
			}
		})
	}
}

func feedAll(s *labelStripper, chunks []string) []string {
	var out []string
	for _, c := range chunks {
		if emit := s.Feed(c); emit != "" {
			out = append(out, emit)
		}
	}
	if held := s.Flush(); held != "" {
		out = append(out, held)
	}
	return out
}

func TestLabelStripperStreaming(t *testing.T) {
	tests := []struct {
		name   string
		agent  string
		chunks []string
		want   string
	}{
		{name: "label split across chunks", agent: "Samantha", chunks: []string{"Sam", "antha: Hi", " there."}, want: "Hi there."},
		{name: "bold label split", agent: "Samantha", chunks: []string{"**Sam", "antha**", ": Hi."}, want: "Hi."},
		{name: "colon in later chunk", agent: "Samantha", chunks: []string{"Samantha", ": Hey."}, want: "Hey."},
		{name: "no label passes through", agent: "Samantha", chunks: []string{"Hel", "lo world."}, want: "Hello world."},
		{name: "short reply flushes verbatim", agent: "Samantha", chunks: []string{"Samantha"}, want: "Samantha"},
		{name: "name without colon released", agent: "Samantha", chunks: []string{"Samantha", " is my name."}, want: "Samantha is my name."},
		{name: "prefix-sharing name released", agent: "Sam", chunks: []string{"Samantha: no."}, want: "Samantha: no."},
		{name: "label only then text", agent: "Samantha", chunks: []string{"Samantha: ", "Hey."}, want: "Hey."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newLabelStripper(tt.agent)
			got := strings.Join(feedAll(s, tt.chunks), "")
			if got != tt.want {
				t.Fatalf("streamed %q, want %q", got, tt.want)
			}
		})
	}
}

// A non-label head must resolve on the first chunk that cannot be the label,
// not sit buffered until end of stream — that would trade the name bug for a
// latency bug in the streamed transcript.
func TestLabelStripperReleasesOrdinaryTextImmediately(t *testing.T) {
	s := newLabelStripper("Samantha")
	if got := s.Feed("So"); got != "So" {
		t.Fatalf("Feed(%q) = %q, want immediate release", "So", got)
	}
	if got := s.Feed(" basically..."); got != " basically..." {
		t.Fatalf("chunk after decision = %q, want verbatim pass-through", got)
	}
}

// The label must vanish from both what streams to the TUI/TTS and what lands
// in history — a labeled assistant turn in history is what teaches the model
// to keep doing it.
func TestOllamaThinkStreamStripsAgentLabel(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Sam"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"antha: Hel"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"lo there."},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{AgentName: "Samantha", MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	var sb strings.Builder
	for c := range stream.Chunks {
		sb.WriteString(c)
	}
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if got := sb.String(); got != "Hello there." {
		t.Fatalf("streamed %q, want %q", got, "Hello there.")
	}
	if got := o.history[len(o.history)-1].Content; got != "Hello there." {
		t.Fatalf("history tail = %q, want %q", got, "Hello there.")
	}
}

func TestClaudeThinkStreamStripsAgentLabel(t *testing.T) {
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("Samantha: Hi"), assistantMsg(" there."), resultMsg("sess-1")}},
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
		t.Fatalf("streamed %q, want %q", text, "Hi there.")
	}
	if got := b.history[len(b.history)-1].Content; got != "Hi there." {
		t.Fatalf("history tail = %q, want %q", got, "Hi there.")
	}
}

func TestGrokThinkStreamStripsAgentLabel(t *testing.T) {
	fake := &fakeGrokClient{
		streamScripts: []grokStreamScript{
			{events: []grok.Event{grokTextEvent("Samantha: Hi", "gsess-1"), grokTextEvent(" there.", "")}},
		},
	}
	g := newTestGrokBrain(fake)

	s := mustGrokStream(t, g, "hello")
	var sb strings.Builder
	for c := range s.Chunks {
		sb.WriteString(c)
	}
	if res := <-s.Done; res.Err != nil {
		t.Fatalf("stream result error = %v", res.Err)
	}
	if got := sb.String(); got != "Hi there." {
		t.Fatalf("streamed %q, want %q", got, "Hi there.")
	}
	if got := g.history[len(g.history)-1].Content; got != "Hi there." {
		t.Fatalf("history tail = %q, want %q", got, "Hi there.")
	}
}
