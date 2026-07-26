package brain

import (
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestCompactInstructionResolvesEmbeddedDefault(t *testing.T) {
	cfg := &config.Config{AgentName: "Samantha", PromptsDir: t.TempDir()}
	text, err := CompactInstruction(cfg)
	if err != nil {
		t.Fatalf("CompactInstruction() error = %v", err)
	}
	if !strings.Contains(text, "Samantha") {
		t.Fatalf("compact prompt did not substitute {agent_name}:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "summar") {
		t.Fatalf("compact prompt does not instruct summarizing:\n%s", text)
	}
}
