package brain

import (
	"reflect"
	"testing"
)

// savedTurns mixes the persisted "assistant"/"tool" scheme with the legacy
// "samantha" role that older claude/grok sessions wrote.
func savedTurns() []Turn {
	return []Turn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling a tool"},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "done"},
		{Role: "samantha", Content: "legacy reply"},
	}
}

func TestLoadHistoryNormalizesRoles(t *testing.T) {
	promptWant := []Turn{
		{Role: "user", Content: "hi"},
		{Role: "samantha", Content: "calling a tool"},
		{Role: "samantha", Content: "done"},
		{Role: "samantha", Content: "legacy reply"},
	}

	tests := []struct {
		name string
		load func([]Turn) []Turn
		want []Turn
	}{
		{
			name: "claude maps assistant to samantha and drops tool results",
			load: func(turns []Turn) []Turn {
				b := &Brain{}
				b.LoadHistory(turns)
				return b.History()
			},
			want: promptWant,
		},
		{
			name: "grok maps assistant to samantha and drops tool results",
			load: func(turns []Turn) []Turn {
				g := &GrokBrain{}
				g.LoadHistory(turns)
				return g.History()
			},
			want: promptWant,
		},
		{
			name: "ollama maps samantha to assistant and keeps tool results",
			load: func(turns []Turn) []Turn {
				o := &OllamaBrain{}
				o.LoadHistory(turns)
				return o.History()
			},
			want: []Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "calling a tool"},
				{Role: "tool", Content: "result"},
				{Role: "assistant", Content: "done"},
				{Role: "assistant", Content: "legacy reply"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.load(savedTurns()); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("history = %+v, want %+v", got, tt.want)
			}
		})
	}
}
