package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// personaEnv points the config/persona roots at a temp dir and seeds a
// samantha profile, so every persona CLI test runs against a real install
// layout instead of a mock.
func personaEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agent_name: Samantha\nactive_persona: samantha\ntts_provider: kokoro\ntts_voice: af_heart\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "samantha", DisplayName: "Samantha", Builtin: true,
		Brain:   persona.Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS:     persona.TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: persona.PromptRefs{Persona: "samantha"},
	})
	return dir
}

func writePersona(t *testing.T, p *persona.Profile) {
	t.Helper()
	if err := persona.Write(p, false); err != nil {
		t.Fatalf("persona.Write(%s): %v", p.ID, err)
	}
}

// writeUserPrompt drops a samantha.prompt.v1 document straight into the
// prompts dir so a test can install the structured mapping form, which the
// persona writers deliberately cannot produce.
func writeUserPrompt(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(config.PromptsDir(), "persona")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const structuredPromptDoc = `schema: samantha.prompt.v1
prompt:
  name: uncle-fu
  kind: persona
  system_prompt:
    identity: You are Uncle Fu.
    guidance:
      - Speak in short sentences.
    constraints:
      - Never mention kung fu movies.
metadata:
  id: uncle-fu-user
  version: 1
`

// runPersona executes the persona command tree exactly as the binary would,
// returning stdout so a test can assert on the JSON a runner would parse.
func runPersona(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPersonaCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	// Separate streams on purpose: the --json contract is that a runner parses
	// stdout alone and never has to scrape stderr.
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func decodePersonaResult(t *testing.T, out string) personaResultJSON {
	t.Helper()
	var got personaResultJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return got
}

func decodeErrorJSON(t *testing.T, out string) (string, string, []string) {
	t.Helper()
	var got struct {
		Error   string   `json:"error"`
		Code    string   `json:"code"`
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding error envelope %q: %v", out, err)
	}
	return got.Code, got.Error, got.Changed
}

func seedUncleFu(t *testing.T) {
	t.Helper()
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "uncle-fu", DisplayName: "Uncle Fu",
		Brain:   persona.Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS:     persona.TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: persona.PromptRefs{Persona: "uncle-fu"},
	})
	if err := persona.WriteSystemPrompt("uncle-fu", "You are Uncle Fu."); err != nil {
		t.Fatal(err)
	}
}

// The UpdateStack wholesale-replace trap: editing only the voice must not
// clear the brain routing the persona was carrying.
func TestPersonaEditVoiceKeepsBrain(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "edit", "uncle-fu", "--voice", "bm_george", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if got.Persona.Brain.Provider != "ollama" || got.Persona.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("brain = %+v, want it untouched", got.Persona.Brain)
	}
	if got.Persona.TTS.Voice != "bm_george" {
		t.Fatalf("tts.voice = %q, want bm_george", got.Persona.TTS.Voice)
	}
	if len(got.Changed) != 1 || got.Changed[0] != "tts.voice" {
		t.Fatalf("changed = %v, want [tts.voice]", got.Changed)
	}
	if got.Applies.Prompt != "next_turn" || got.Applies.Stack != "next_conversation" {
		t.Fatalf("applies = %+v, want next_turn/next_conversation", got.Applies)
	}

	// The reported profile must match what a fresh load sees.
	reloaded, err := persona.Load("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Brain.Model != "qwen2.5:14b" || reloaded.TTS.Voice != "bm_george" {
		t.Fatalf("on disk = %+v, want brain kept and voice updated", reloaded)
	}
}

// An explicitly empty value is a clear-to-inherit, not a no-op.
func TestPersonaEditEmptyVoiceClearsToInherit(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "edit", "uncle-fu", "--voice", "", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if got.Persona.TTS.Voice != "" {
		t.Fatalf("tts.voice = %q, want cleared", got.Persona.TTS.Voice)
	}
	if len(got.Changed) != 1 || got.Changed[0] != "tts.voice" {
		t.Fatalf("changed = %v, want [tts.voice]", got.Changed)
	}
}

func TestPersonaEditNoOpReportsNothingChanged(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "edit", "uncle-fu", "--voice", "af_heart", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if len(got.Changed) != 0 {
		t.Fatalf("changed = %v, want empty for a no-op edit", got.Changed)
	}
}

func TestPersonaEditErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode string
		wantIn   string
	}{
		{
			name:     "unknown id",
			args:     []string{"edit", "ghost", "--voice", "af_heart", "--json"},
			wantCode: codeNotFound,
			wantIn:   "uncle-fu",
		},
		{
			name:     "bad id shape",
			args:     []string{"edit", "Uncle Fu", "--voice", "af_heart", "--json"},
			wantCode: codeInvalidID,
		},
		{
			name:     "bad tier",
			args:     []string{"edit", "uncle-fu", "--tier", "3b", "--json"},
			wantCode: codeInvalidTier,
		},
		{
			name:     "empty prompt",
			args:     []string{"edit", "uncle-fu", "--prompt", "   ", "--json"},
			wantCode: codePromptEmpty,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			personaEnv(t)
			seedUncleFu(t)

			out, err := runPersona(t, tc.args...)
			if err == nil {
				t.Fatalf("persona edit error = nil, want %s (out %s)", tc.wantCode, out)
			}
			code, msg, _ := decodeErrorJSON(t, out)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q (error %q)", code, tc.wantCode, msg)
			}
			if tc.wantIn != "" && !strings.Contains(msg, tc.wantIn) {
				t.Fatalf("error = %q, want it to name %q", msg, tc.wantIn)
			}
		})
	}
}

// A structured document is the one place a flat write destroys user content,
// so it is refused and the file must be byte-identical afterwards.
func TestPersonaEditRefusesStructuredPrompt(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	path := writeUserPrompt(t, "uncle-fu", structuredPromptDoc)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "edit", "uncle-fu", "--prompt", "You are a pirate.", "--json")
	if err == nil {
		t.Fatalf("persona edit error = nil, want prompt_structured (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codePromptStructured {
		t.Fatalf("code = %q, want %q", code, codePromptStructured)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("document changed on a refused edit:\n%s", after)
	}
}

func TestPersonaEditAllowFlattenWritesPrompt(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	path := writeUserPrompt(t, "uncle-fu", structuredPromptDoc)

	out, err := runPersona(t, "edit", "uncle-fu", "--prompt", "You are a pirate.", "--allow-flatten", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if len(got.Changed) != 1 || got.Changed[0] != "prompt" {
		t.Fatalf("changed = %v, want [prompt]", got.Changed)
	}
	if got.Prompt == nil || !got.Prompt.Written || got.Prompt.Structured {
		t.Fatalf("prompt = %+v, want written and no longer structured", got.Prompt)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "pirate") || strings.Contains(string(raw), "guidance") {
		t.Fatalf("document = %s, want the flat pirate body", raw)
	}
}

func TestPersonaEditPromptFile(t *testing.T) {
	dir := personaEnv(t)
	seedUncleFu(t)
	file := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(file, []byte("You are Uncle Fu, a patient teacher.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "edit", "uncle-fu", "--prompt-file", file, "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if len(got.Changed) != 1 || got.Changed[0] != "prompt" {
		t.Fatalf("changed = %v, want [prompt]", got.Changed)
	}
	body, err := persona.LoadSystemPrompt("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "patient teacher") {
		t.Fatalf("prompt body = %q, want the file contents", body)
	}
}

// All six edit flags in one call, so the merge is exercised as a whole rather
// than one field at a time.
func TestPersonaEditAllFlags(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "edit", "uncle-fu",
		"--display-name", "Uncle Fu Senior",
		"--brain-provider", "grok", "--brain-model", "grok-4",
		"--tts-provider", "qwen3-tts", "--voice", "Uncle_Fu", "--tier", "1.7",
		"--prompt", "You are Uncle Fu Senior.",
		"--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	want := []string{"display_name", "brain.provider", "brain.model", "tts.provider", "tts.voice", "tts.tier", "prompt"}
	if strings.Join(got.Changed, ",") != strings.Join(want, ",") {
		t.Fatalf("changed = %v, want %v", got.Changed, want)
	}
	if got.Persona.TTS.Tier != "1.7b" {
		t.Fatalf("tts.tier = %q, want the canonical 1.7b", got.Persona.TTS.Tier)
	}
	if got.Persona.DisplayName != "Uncle Fu Senior" {
		t.Fatalf("display_name = %q", got.Persona.DisplayName)
	}
}

// The json tags on Profile are the contract: no Go field names on the wire.
func TestPersonaShowJSONIsSnakeCase(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "show", "uncle-fu", "--json")
	if err != nil {
		t.Fatalf("persona show error = %v (out %s)", err, out)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	var profile map[string]any
	if err := json.Unmarshal(envelope["persona"], &profile); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema", "id", "display_name", "builtin", "brain", "tts", "prompts", "path"} {
		if _, ok := profile[key]; !ok {
			t.Errorf("persona object missing %q (keys %v)", key, profile)
		}
	}
	for _, key := range []string{"ID", "DisplayName", "Brain", "TTS", "Prompts", "Path", "Schema"} {
		if _, ok := profile[key]; ok {
			t.Errorf("persona object still encodes the Go field name %q", key)
		}
	}
	if got := profile["tts"].(map[string]any)["tier"]; got != "" {
		t.Errorf("tts.tier = %v, want an empty string rather than an absent key", got)
	}
}

func TestPersonaShowWithPrompt(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "show", "uncle-fu", "--json", "--with-prompt")
	if err != nil {
		t.Fatalf("persona show error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if got.Prompt == nil {
		t.Fatal("prompt object missing")
	}
	if got.Prompt.Source != "user" || got.Prompt.Body == nil || *got.Prompt.Body != "You are Uncle Fu." {
		t.Fatalf("prompt = %+v, want the user body", got.Prompt)
	}
	if len(got.Prompt.Hash) != persona.PromptHashPrefix {
		t.Fatalf("prompt.hash = %q, want %d hex chars", got.Prompt.Hash, persona.PromptHashPrefix)
	}
}

// A structured or embedded document is not safe to flat-edit, so the editor
// gets an empty body rather than a lossy rendering of one.
func TestPersonaShowWithPromptEmptyBodyForStructured(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	writeUserPrompt(t, "uncle-fu", structuredPromptDoc)

	out, err := runPersona(t, "show", "uncle-fu", "--json", "--with-prompt")
	if err != nil {
		t.Fatalf("persona show error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if got.Prompt == nil || !got.Prompt.Structured {
		t.Fatalf("prompt = %+v, want structured", got.Prompt)
	}
	if got.Prompt.Body == nil || *got.Prompt.Body != "" {
		t.Fatalf("prompt.body = %v, want empty for a structured document", got.Prompt.Body)
	}
}

func TestPersonaShowActiveFlag(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "show", "samantha", "--json")
	if err != nil {
		t.Fatalf("persona show error = %v (out %s)", err, out)
	}
	if got := decodePersonaResult(t, out); !got.Active {
		t.Fatal("active = false for the configured active persona")
	}
	out, err = runPersona(t, "show", "uncle-fu", "--json")
	if err != nil {
		t.Fatalf("persona show error = %v (out %s)", err, out)
	}
	if got := decodePersonaResult(t, out); got.Active {
		t.Fatal("active = true for a non-active persona")
	}
}

// Writing a body binds the profile to its own document. The shared one the
// persona used to reference stays on disk, and changed[] names the ref that
// actually moved.
func TestPersonaEditPromptRepointsSharedRef(t *testing.T) {
	personaEnv(t)
	if err := persona.WriteSystemPrompt("house-style", "You are a house-style agent."); err != nil {
		t.Fatal(err)
	}
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "borrower", DisplayName: "Borrower",
		TTS:     persona.TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts: persona.PromptRefs{Persona: "house-style"},
	})
	shared := filepath.Join(config.PromptsDir(), "persona", "house-style.yaml")
	before, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "edit", "borrower", "--prompt", "You are Borrower.", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	want := []string{"prompt", "prompts.persona"}
	if strings.Join(got.Changed, ",") != strings.Join(want, ",") {
		t.Fatalf("changed = %v, want %v", got.Changed, want)
	}
	if got.Persona.Prompts.Persona != "borrower" {
		t.Fatalf("prompts.persona = %q, want borrower", got.Persona.Prompts.Persona)
	}
	after, err := os.ReadFile(shared)
	if err != nil {
		t.Fatalf("shared document gone: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("shared document rewritten:\n%s", after)
	}
}

// An edit that passes no stack flag reports no stack change, even when the
// profile on disk carries stray whitespace UpdateStack would trim.
func TestPersonaEditIgnoresWhitespaceOnlyStackDrift(t *testing.T) {
	personaEnv(t)
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "uncle-fu", DisplayName: "Uncle Fu",
		Brain:   persona.Brain{Provider: "ollama ", Model: " qwen2.5:14b"},
		TTS:     persona.TTS{Provider: "kokoro", Voice: "af_heart "},
		Prompts: persona.PromptRefs{Persona: "uncle-fu"},
	})
	if err := persona.WriteSystemPrompt("uncle-fu", "You are Uncle Fu."); err != nil {
		t.Fatal(err)
	}

	out, err := runPersona(t, "edit", "uncle-fu", "--display-name", "Uncle Fu Senior", "--json")
	if err != nil {
		t.Fatalf("persona edit error = %v (out %s)", err, out)
	}
	got := decodePersonaResult(t, out)
	if len(got.Changed) != 1 || got.Changed[0] != "display_name" {
		t.Fatalf("changed = %v, want only [display_name]", got.Changed)
	}
}
