package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/brain"
)

// TestDefaultPersonaParity is the compatibility contract: the embedded
// default persona, resolved with an agent name, must reproduce
// brain.GetSystemPrompt byte-for-byte.
func TestDefaultPersonaParity(t *testing.T) {
	doc, err := Default(KindPersona)
	if err != nil {
		t.Fatalf("Default(persona) error = %v", err)
	}
	got, err := ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": "TestAgent"})
	if err != nil {
		t.Fatalf("ResolvePlaceholders() error = %v", err)
	}
	want := brain.GetSystemPrompt("TestAgent")
	if got != want {
		t.Errorf("embedded persona diverges from brain.GetSystemPrompt at byte %d:\ngot:  %q\nwant: %q", firstDiff(got, want), got, want)
	}
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func TestDefaultUnknownKind(t *testing.T) {
	if _, err := Default(KindPronunciation); err == nil || !strings.Contains(err.Error(), "no embedded default") {
		t.Errorf("Default(pronunciation) error = %v, want no-embedded-default error", err)
	}
}

func TestResolverPrecedence(t *testing.T) {
	userDir := t.TempDir()
	personaDir := filepath.Join(userDir, "persona")
	if err := os.MkdirAll(personaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userDoc := `schema: samantha.prompt.v1
prompt:
  name: samantha
  kind: persona
  system_prompt: User-dir persona.
`
	if err := os.WriteFile(filepath.Join(personaDir, "samantha.yaml"), []byte(userDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	explicitDir := t.TempDir()
	explicitPath := filepath.Join(explicitDir, "explicit.yaml")
	explicitDoc := `schema: samantha.prompt.v1
prompt:
  name: samantha
  kind: persona
  system_prompt: Explicit persona.
`
	if err := os.WriteFile(explicitPath, []byte(explicitDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	embedded, err := Default(KindPersona)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		resolver     Resolver
		kind         Kind
		promptName   string
		wantIdentity string
	}{
		{
			name:         "explicit path wins over user dir",
			resolver:     Resolver{Path: explicitPath, UserDir: userDir},
			kind:         KindPersona,
			promptName:   "samantha",
			wantIdentity: "Explicit persona.",
		},
		{
			name:         "user dir wins over embedded",
			resolver:     Resolver{UserDir: userDir},
			kind:         KindPersona,
			promptName:   "samantha",
			wantIdentity: "User-dir persona.",
		},
		{
			name:         "user dir miss falls back to embedded",
			resolver:     Resolver{UserDir: userDir},
			kind:         KindPersona,
			promptName:   "other",
			wantIdentity: embedded.Prompt.SystemPrompt.Identity,
		},
		{
			name:         "no layers configured uses embedded",
			resolver:     Resolver{},
			kind:         KindPersona,
			promptName:   "samantha",
			wantIdentity: embedded.Prompt.SystemPrompt.Identity,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := tt.resolver.Resolve(tt.kind, tt.promptName)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if doc.Prompt.SystemPrompt.Identity != tt.wantIdentity {
				t.Errorf("Resolve() identity = %q, want %q", doc.Prompt.SystemPrompt.Identity, tt.wantIdentity)
			}
		})
	}
}

func TestResolverFindsMarkdownInUserDir(t *testing.T) {
	userDir := t.TempDir()
	styleDir := filepath.Join(userDir, "style")
	if err := os.MkdirAll(styleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(styleDir, "casual.md"), []byte("Speak casually.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := Resolver{UserDir: userDir}.Resolve(KindStyle, "casual")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if doc.Prompt.Kind != KindStyle || doc.Prompt.Name != "casual" {
		t.Errorf("Resolve() prompt = %+v, want kind style name casual", doc.Prompt)
	}
	if doc.Prompt.SystemPrompt.Identity != "Speak casually.\n" {
		t.Errorf("identity = %q, want the markdown content", doc.Prompt.SystemPrompt.Identity)
	}
}

func TestResolverMissEverywhere(t *testing.T) {
	if _, err := (Resolver{UserDir: t.TempDir()}).Resolve(KindStyle, "nope"); err == nil {
		t.Error("Resolve() succeeded, want error when no layer has the document")
	}
}
