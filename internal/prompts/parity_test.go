package prompts_test

import (
	"os"
	"strings"
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
	wantText := strings.TrimSuffix(string(want), "\n")
	if got != wantText {
		t.Errorf("assembled persona diverges from golden at byte %d:\ngot:  %q\nwant: %q", firstDiff(got, wantText), got, wantText)
	}
}

// TestDefaultTurnGolden pins the embedded per-turn instruction against
// accidental drift.
//
// The migration-safety contract is unchanged and still checked: the instruction
// must still OPEN with the exact literal the Claude and Grok paths appended
// before the loader wiring. What may follow it is a short, explicitly enumerated
// set of deliberate additions — so an accidental edit still fails, while an
// intentional one has to be declared here and justified in turn.yaml's metadata.
func TestDefaultTurnGolden(t *testing.T) {
	got := resolveDefault(t, prompts.KindTurn, "Samantha")

	const original = "Respond as Samantha. 2-3 sentences max, natural speech, NO markdown, NO formatting, NO code blocks, NO bullet points. Just talk naturally."
	if !strings.HasPrefix(got, original) {
		t.Fatalf("assembled turn instruction no longer opens with the original literal, "+
			"diverging at byte %d:\ngot:  %q\nwant prefix: %q", firstDiff(got, original), got, original)
	}

	// Every deliberate addition, newest last. Adding a line here is the point at
	// which someone has to justify changing what every provider is told.
	additions := []string{
		// R-L3: first audio waits for the first COMPLETE sentence to synthesize,
		// and Kokoro runs at ~1.32x realtime, so a shorter opening reaches the
		// listener sooner. The second clause frees the rest of the reply on
		// purpose — the failure mode of this instruction is a curt agent.
		"Make your first sentence a short one — a dozen words at most — then say the rest however it needs to be said.",
	}
	want := original
	for _, add := range additions {
		want += "\n" + add
	}
	if got != want {
		t.Errorf("assembled turn instruction has an undeclared change at byte %d:\ngot:  %q\nwant: %q",
			firstDiff(got, want), got, want)
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
