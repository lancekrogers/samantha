package brain

import (
	"testing"

	"github.com/ollama/ollama/api"
)

func TestSessionInfoPerProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider SessionReporter
		want     SessionState
	}{
		{
			name: "claude harness with live session",
			provider: func() *Brain {
				b := newTestBrain(&fakeClaudeClient{})
				b.sessionID = "sess-12345678"
				b.promptTokens = 12340
				b.history = []Turn{{Role: "user", Content: "hi"}, {Role: "samantha", Content: "hey"}}
				return b
			}(),
			want: SessionState{Kind: "harness", ID: "sess-12345678", PromptTokens: 12340, Turns: 2},
		},
		{
			name:     "claude harness before first turn",
			provider: newTestBrain(&fakeClaudeClient{}),
			want:     SessionState{Kind: "harness"},
		},
		{
			name: "grok harness has no session wired",
			provider: &GrokBrain{
				history: []Turn{{Role: "user", Content: "hi"}},
			},
			want: SessionState{Kind: "harness", Turns: 1},
		},
		{
			name: "ollama chat estimates next request",
			provider: &OllamaBrain{
				fullSystemPrompt: "0123",
				history:          []api.Message{{Role: "user", Content: "0123456789ab"}},
			},
			// (4 + 12 + 16) / 4 = 8
			want: SessionState{Kind: "chat", PromptTokens: 8, Turns: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.provider.SessionInfo(); got != tt.want {
				t.Fatalf("SessionInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
