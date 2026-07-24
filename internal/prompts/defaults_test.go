package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
			name:         "empty name uses embedded default for kind",
			resolver:     Resolver{UserDir: userDir},
			kind:         KindPersona,
			promptName:   "",
			wantIdentity: embedded.Prompt.SystemPrompt.Identity,
		},
		{
			name:         "no layers configured uses embedded when name matches",
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

func TestResolverDoesNotInjectWrongPersona(t *testing.T) {
	// Regression: missing "uncle-fu" used to return the embedded samantha
	// persona, so the Personas editor and brain silently showed the wrong identity.
	userDir := t.TempDir()
	_, err := Resolver{UserDir: userDir}.Resolve(KindPersona, "uncle-fu")
	if err == nil {
		t.Fatal("Resolve(persona, uncle-fu) succeeded, want missing-document error")
	}
	if !strings.Contains(err.Error(), "uncle-fu") {
		t.Fatalf("error = %q, want it to name uncle-fu", err)
	}
	// Embedded default still answers under its real name.
	doc, err := Resolver{UserDir: userDir}.Resolve(KindPersona, "samantha")
	if err != nil {
		t.Fatalf("Resolve(samantha) error = %v", err)
	}
	if doc.Prompt.Name != "samantha" {
		t.Fatalf("name = %q, want samantha", doc.Prompt.Name)
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
