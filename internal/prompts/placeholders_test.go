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

func TestPersonaPlaceholderCatalog(t *testing.T) {
	names := PlaceholderNames()
	if len(names) == 0 {
		t.Fatal("PlaceholderNames empty")
	}
	if !IsKnownPlaceholder("agent_name") {
		t.Fatal("agent_name should be known")
	}
	if IsKnownPlaceholder("planet") {
		t.Fatal("planet should not be known")
	}
	vals := PlaceholderValues("Sam")
	if vals["agent_name"] != "Sam" {
		t.Fatalf("values = %#v", vals)
	}
	// Catalog round-trips through ResolvePlaceholders.
	got, err := ResolvePlaceholders("Hi {agent_name}.", names, vals)
	if err != nil || got != "Hi Sam." {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFilterPlaceholders(t *testing.T) {
	all := FilterPlaceholders("")
	if len(all) != len(PersonaPlaceholders) {
		t.Fatalf("empty prefix = %v", all)
	}
	got := FilterPlaceholders("agent")
	if len(got) != 1 || got[0] != "agent_name" {
		t.Fatalf("prefix agent = %v", got)
	}
	if got := FilterPlaceholders("zzz"); len(got) != 0 {
		t.Fatalf("zzz = %v", got)
	}
}

func TestFindPlaceholders(t *testing.T) {
	line := "You are {agent_name}, not {planet}."
	spans := FindPlaceholders(line)
	if len(spans) != 2 {
		t.Fatalf("spans = %v", spans)
	}
	if line[spans[0][0]:spans[0][1]] != "{agent_name}" {
		t.Fatalf("first = %q", line[spans[0][0]:spans[0][1]])
	}
	if PlaceholderNameAt(line, spans[0][0]+1) != "agent_name" {
		t.Fatalf("name at = %q", PlaceholderNameAt(line, spans[0][0]+1))
	}
}
