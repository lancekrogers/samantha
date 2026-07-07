package prompts

import (
	"strings"
	"testing"
)

func TestResolvePlaceholders(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		allowed         []string
		values          map[string]string
		want            string
		wantErrContains string
	}{
		{
			name:            "unknown placeholder",
			text:            "You are {agent_name} on {planet}.",
			allowed:         []string{"agent_name"},
			values:          map[string]string{"agent_name": "Sam"},
			wantErrContains: "unknown placeholder {planet}",
		},
		{
			name:            "unresolved placeholder",
			text:            "You are {agent_name}.",
			allowed:         []string{"agent_name"},
			values:          map[string]string{},
			wantErrContains: "unresolved placeholder {agent_name}",
		},
		{
			name:    "single token",
			text:    "You are {agent_name}.",
			allowed: []string{"agent_name"},
			values:  map[string]string{"agent_name": "Sam"},
			want:    "You are Sam.",
		},
		{
			name:    "repeated and multiple tokens",
			text:    "{agent_name} runs in {work_dir}. Hi {agent_name}.",
			allowed: []string{"agent_name", "work_dir"},
			values:  map[string]string{"agent_name": "Sam", "work_dir": "/tmp"},
			want:    "Sam runs in /tmp. Hi Sam.",
		},
		{
			name:    "no tokens",
			text:    "Plain text with no braces.",
			allowed: nil,
			values:  nil,
			want:    "Plain text with no braces.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolvePlaceholders(tt.text, tt.allowed, tt.values)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("ResolvePlaceholders() succeeded, want error containing %q", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePlaceholders() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolvePlaceholders() = %q, want %q", got, tt.want)
			}
		})
	}
}
