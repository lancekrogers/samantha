package prompts_test

import (
	"os"
	"testing"

	"github.com/lancekrogers/samantha/internal/prompts"
)

// TestDefaultPersonaGolden pins the embedded persona: assembled and resolved
// with an agent name, it must equal the checked-in golden captured from the
// original hard-coded brain.GetSystemPrompt before the loader wiring. This is
// the migration-safety contract — the default persona must not drift.
func TestDefaultPersonaGolden(t *testing.T) {
	got := resolveDefault(t, prompts.KindPersona, "TestAgent")

	want, err := os.ReadFile("testdata/persona.golden")
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("assembled persona diverges from golden at byte %d:\ngot:  %q\nwant: %q", firstDiff(got, string(want)), got, string(want))
	}
}

// TestDefaultTurnGolden pins the embedded per-turn instruction to the exact
// literal the Claude and Grok paths appended before the loader wiring.
func TestDefaultTurnGolden(t *testing.T) {
	got := resolveDefault(t, prompts.KindTurn, "Samantha")

	const want = "Respond as Samantha. 2-3 sentences max, natural speech, NO markdown, NO formatting, NO code blocks, NO bullet points. Just talk naturally."
	if got != want {
		t.Errorf("assembled turn instruction diverges from the original literal at byte %d:\ngot:  %q\nwant: %q", firstDiff(got, want), got, want)
	}
}

func resolveDefault(t *testing.T, kind prompts.Kind, agentName string) string {
	t.Helper()
	doc, err := prompts.Default(kind)
	if err != nil {
		t.Fatalf("Default(%s) error = %v", kind, err)
	}
	got, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": agentName})
	if err != nil {
		t.Fatalf("ResolvePlaceholders() error = %v", err)
	}
	return got
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
