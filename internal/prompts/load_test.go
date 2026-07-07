package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validDoc = `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt: You are {agent_name}.
`

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name            string
		doc             string
		wantErrContains string
	}{
		{
			name: "unknown top-level key",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt: hi
extra: nope
`,
			wantErrContains: "extra",
		},
		{
			name: "unknown prompt key",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt: hi
  voice: nope
`,
			wantErrContains: "voice",
		},
		{
			name: "unknown system_prompt key",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt:
    identity: hi
    tone: nope
`,
			wantErrContains: `unknown key "tone"`,
		},
		{
			name: "missing identity in object form",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt:
    guidance:
      - Be brief.
`,
			wantErrContains: "missing identity",
		},
		{
			name: "empty identity in string form",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt: ""
`,
			wantErrContains: "missing identity",
		},
		{
			name: "bad schema",
			doc: `schema: samantha.prompt.v2
prompt:
  name: test
  kind: persona
  system_prompt: hi
`,
			wantErrContains: `schema "samantha.prompt.v2", want "samantha.prompt.v1"`,
		},
		{
			name: "unknown kind",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: vibe
  system_prompt: hi
`,
			wantErrContains: `unknown kind "vibe"`,
		},
		{
			name: "empty name",
			doc: `schema: samantha.prompt.v1
prompt:
  kind: persona
  system_prompt: hi
`,
			wantErrContains: "missing prompt.name",
		},
		{
			name: "system_prompt as sequence",
			doc: `schema: samantha.prompt.v1
prompt:
  name: test
  kind: persona
  system_prompt:
    - hi
`,
			wantErrContains: "must be a string or a mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.doc))
			if err == nil {
				t.Fatalf("Load() succeeded, want error containing %q", tt.wantErrContains)
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErrContains)
			}
		})
	}
}

func TestLoadStringForm(t *testing.T) {
	doc, err := Load([]byte(validDoc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.Prompt.Name != "test" || doc.Prompt.Kind != KindPersona {
		t.Errorf("Load() prompt = %+v, want name test kind persona", doc.Prompt)
	}
	if doc.Prompt.SystemPrompt.Identity != "You are {agent_name}." {
		t.Errorf("identity = %q, want the string verbatim", doc.Prompt.SystemPrompt.Identity)
	}
}

func TestLoadFileMarkdownInterop(t *testing.T) {
	dir := t.TempDir()
	identity := "You are {agent_name}, a markdown persona.\n"
	mdPath := filepath.Join(dir, "custom.md")
	if err := os.WriteFile(mdPath, []byte(identity), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := LoadFile(mdPath, KindPersona)
	if err != nil {
		t.Fatalf("LoadFile(md) error = %v", err)
	}
	if doc.Prompt.Name != "custom" || doc.Prompt.Kind != KindPersona {
		t.Errorf("md document = %+v, want name custom kind persona", doc.Prompt)
	}

	// The same identity in a YAML document must hash identically.
	yamlDoc := "schema: samantha.prompt.v1\nprompt:\n  name: custom\n  kind: persona\n  system_prompt: |\n    You are {agent_name}, a markdown persona.\n"
	fromYAML, err := Load([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("Load(yaml) error = %v", err)
	}
	if doc.Hash() != fromYAML.Hash() {
		t.Errorf("md hash %s != yaml hash %s for the same identity", doc.Hash(), fromYAML.Hash())
	}
}

func TestLoadFileKindMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(validDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path, KindStyle); err == nil || !strings.Contains(err.Error(), `kind "persona", want "style"`) {
		t.Errorf("LoadFile() error = %v, want kind mismatch", err)
	}
}
