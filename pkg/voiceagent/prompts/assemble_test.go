package prompts

import (
	"os"
	"testing"
)

// TestAssembleSectionOrder locks the pinned section order and rendering of
// the structured form. Changing this output changes prompt hashes, which
// future resume keys depend on.
func TestAssembleSectionOrder(t *testing.T) {
	data, err := os.ReadFile("testdata/structured.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Load(data)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := "You are {agent_name}, an example assistant used to exercise every assembly section." +
		"\n\nConversation style:\n- warm\n- direct" +
		"\n\nGuidance:\n- Keep responses short.\n- Offer to go deeper on complex topics." +
		"\n\nConstraints:\n- Never use markdown.\n- Never use emojis." +
		"\n\nCore concepts:\nbrevity: Say less, mean more.\ncompanionship: You're a companion, not a servant."
	if got := doc.Assemble(); got != want {
		t.Errorf("Assemble() = %q, want %q", got, want)
	}
}

func TestAssembleIdentityOnly(t *testing.T) {
	doc := &Document{
		Schema: Schema,
		Prompt: Prompt{Name: "t", Kind: KindPersona, SystemPrompt: SystemPrompt{Identity: "Just identity.\n"}},
	}
	if got := doc.Assemble(); got != "Just identity." {
		t.Errorf("Assemble() = %q, want identity with trailing newline trimmed", got)
	}
}

func TestHashStabilityAcrossLoads(t *testing.T) {
	data, err := os.ReadFile("testdata/structured.yaml")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash() != second.Hash() {
		t.Errorf("hash differs across loads: %s vs %s", first.Hash(), second.Hash())
	}
	if len(first.Hash()) != 64 {
		t.Errorf("Hash() = %q, want 64 hex chars", first.Hash())
	}
}
