package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func decodeCreateResult(t *testing.T, out string) personaCreateJSON {
	t.Helper()
	var got personaCreateJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return got
}

func TestPersonaCreateJSONActivates(t *testing.T) {
	personaEnv(t)

	out, err := runPersona(t, "create", "Test Fu", "--json")
	if err != nil {
		t.Fatalf("persona create error = %v (out %s)", err, out)
	}
	got := decodeCreateResult(t, out)
	if !got.Created || !got.Activated {
		t.Fatalf("result = %+v, want created and activated", got)
	}
	if got.Persona.ID != "test-fu" || got.Persona.DisplayName != "Test Fu" {
		t.Fatalf("persona = %+v, want the slugged id", got.Persona)
	}
	if got.Prompt != nil {
		t.Fatalf("prompt = %+v, want it absent when no prompt flag was passed", got.Prompt)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persona.ActiveID(cfg) != "test-fu" {
		t.Fatalf("active_persona = %q, want test-fu", persona.ActiveID(cfg))
	}
}

// --no-activate is the whole point of the flag: a front end must be able to
// create a persona without hijacking the running agent.
func TestPersonaCreateNoActivateLeavesActivePersona(t *testing.T) {
	personaEnv(t)

	out, err := runPersona(t, "create", "Test Fu", "--no-activate", "--json")
	if err != nil {
		t.Fatalf("persona create error = %v (out %s)", err, out)
	}
	got := decodeCreateResult(t, out)
	if !got.Created || got.Activated {
		t.Fatalf("result = %+v, want created but not activated", got)
	}
	if _, err := persona.Load("test-fu"); err != nil {
		t.Fatalf("persona not written: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persona.ActiveID(cfg) != "samantha" {
		t.Fatalf("active_persona = %q, want it unchanged", persona.ActiveID(cfg))
	}
}

func TestPersonaCreatePromptFile(t *testing.T) {
	dir := personaEnv(t)
	file := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(file, []byte("You are Test Fu, a careful editor.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "create", "Test Fu", "--prompt-file", file, "--json")
	if err != nil {
		t.Fatalf("persona create error = %v (out %s)", err, out)
	}
	got := decodeCreateResult(t, out)
	if got.Prompt == nil || !got.Prompt.Written || got.Prompt.Source != "user" {
		t.Fatalf("prompt = %+v, want a written user document", got.Prompt)
	}
	body, err := persona.LoadSystemPrompt("test-fu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "careful editor") {
		t.Fatalf("prompt body = %q, want the file contents", body)
	}
}

func TestPersonaCreateEmptyPromptRejected(t *testing.T) {
	personaEnv(t)

	out, err := runPersona(t, "create", "Test Fu", "--prompt", "  ", "--json")
	if err == nil {
		t.Fatalf("persona create error = nil, want prompt_empty (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codePromptEmpty {
		t.Fatalf("code = %q, want %q", code, codePromptEmpty)
	}
	if _, err := persona.Load("test-fu"); err == nil {
		t.Fatal("persona created despite a rejected prompt")
	}
}

// Without --json the output must stay exactly what it was, so scripts and the
// TUI's shell-outs are unaffected.
func TestPersonaCreateWithoutJSONKeepsHumanOutput(t *testing.T) {
	personaEnv(t)

	out, err := runPersona(t, "create", "Test Fu")
	if err != nil {
		t.Fatalf("persona create error = %v (out %s)", err, out)
	}
	for _, want := range []string{"Created persona: Test Fu (test-fu)", "Active now."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "\"created\"") {
		t.Fatalf("output = %q, want no JSON without --json", out)
	}
}

func TestPersonaUseJSON(t *testing.T) {
	personaEnv(t)
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "uncle-fu", DisplayName: "Uncle Fu",
		Brain:   persona.Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS:     persona.TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"},
		Prompts: persona.PromptRefs{Persona: "uncle-fu"},
	})
	if err := persona.WriteSystemPrompt("uncle-fu", "You are Uncle Fu."); err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "use", "uncle-fu", "--json")
	if err != nil {
		t.Fatalf("persona use error = %v (out %s)", err, out)
	}
	var got personaUseJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.ActivePersona != "uncle-fu" || got.DisplayName != "Uncle Fu" {
		t.Fatalf("result = %+v", got)
	}
	if got.Brain.Provider != "ollama" || got.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("brain = %+v, want the persona's routing", got.Brain)
	}
	// Qwen keeps its voice in a separate config key: reading tts_voice here
	// would report the Kokoro voice the persona never uses.
	if got.TTS.Provider != "qwen3-tts" || got.TTS.Voice != "Uncle_Fu" || got.TTS.Tier != "1.7b" {
		t.Fatalf("tts = %+v, want the qwen keys", got.TTS)
	}
}

func TestPersonaUseUnknownID(t *testing.T) {
	personaEnv(t)

	out, err := runPersona(t, "use", "ghost", "--json")
	if err == nil {
		t.Fatalf("persona use error = nil, want not_found (out %s)", out)
	}
	code, msg, _ := decodeErrorJSON(t, out)
	if code != codeNotFound {
		t.Fatalf("code = %q, want %q (error %q)", code, codeNotFound, msg)
	}
}

func TestPersonaUseWithoutJSONKeepsHumanOutput(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "use", "uncle-fu")
	if err != nil {
		t.Fatalf("persona use error = %v (out %s)", err, out)
	}
	if !strings.Contains(out, "Active persona: uncle-fu (Uncle Fu)") {
		t.Fatalf("output = %q", out)
	}
}
