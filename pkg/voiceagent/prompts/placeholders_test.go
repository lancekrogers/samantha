package prompts

import (
	"strings"
	"testing"
	"time"
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
	vals := PlaceholderValues("Sam", PlaceholderEnv{})
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

// The acceptance criterion for {date}: a persona using it must speak today's
// date after midnight without a restart. That only holds because prompts are
// resolved per turn — resolved once at construction, the value would be stamped
// on the process and wrong by the next morning.
func TestDatePlaceholderTracksTheClock(t *testing.T) {
	before := PlaceholderValues("Sam", PlaceholderEnv{
		Now: time.Date(2026, 8, 11, 23, 59, 0, 0, time.UTC),
	})["date"]
	after := PlaceholderValues("Sam", PlaceholderEnv{
		Now: time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC),
	})["date"]

	if before == after {
		t.Fatalf("date did not roll over midnight: both %q", before)
	}
	if !strings.Contains(before, "11 August 2026") || !strings.Contains(after, "12 August 2026") {
		t.Errorf("unexpected formatting: before=%q after=%q", before, after)
	}
}

// Every catalogued placeholder must have a value, or a persona using a name the
// editor offered would fail to resolve at runtime.
func TestEveryCataloguedPlaceholderHasAValue(t *testing.T) {
	vals := PlaceholderValues("Sam", PlaceholderEnv{})
	for _, p := range PersonaPlaceholders {
		v, ok := vals[p.Name]
		if !ok {
			t.Errorf("placeholder %q is offered by the catalog but has no value", p.Name)
			continue
		}
		if strings.TrimSpace(v) == "" {
			t.Errorf("placeholder %q resolves to empty", p.Name)
		}
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("placeholder %q has no description; the editor surfaces these", p.Name)
		}
	}
}

// Unknown names must still fail loudly rather than passing through — silent
// passthrough would ship a literal "{typo}" to the model.
func TestUnknownPlaceholderStillFailsLoudly(t *testing.T) {
	_, err := ResolvePlaceholders("Hello {nonsense}", PlaceholderNames(), PlaceholderValues("Sam", PlaceholderEnv{}))
	if err == nil {
		t.Fatal("expected an unknown-placeholder error")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("error should name the offending placeholder, got %v", err)
	}
}
