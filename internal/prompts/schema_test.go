package prompts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestPromptJSONSchema keeps the editor-facing JSON schema present and
// parseable; runtime validation is the Go code in this package.
func TestPromptJSONSchema(t *testing.T) {
	data, err := os.ReadFile("../../docs/schemas/prompt.schema.json")
	if err != nil {
		t.Fatalf("reading prompt.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("prompt.schema.json is not valid JSON: %v", err)
	}
	if !strings.Contains(string(data), Schema) {
		t.Errorf("prompt.schema.json does not mention %q", Schema)
	}
}
