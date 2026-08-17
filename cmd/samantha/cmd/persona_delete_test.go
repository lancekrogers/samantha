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

func decodeDeleteResult(t *testing.T, out string) personaDeleteJSON {
	t.Helper()
	var got personaDeleteJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return got
}

func TestPersonaDeleteRefusals(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode string
	}{
		{
			name:     "builtin is protected",
			setup:    seedUncleFu,
			args:     []string{"delete", "samantha", "--yes", "--json"},
			wantCode: codeBuiltinProtected,
		},
		{
			name:     "confirmation required",
			setup:    seedUncleFu,
			args:     []string{"delete", "uncle-fu", "--json"},
			wantCode: codeConfirmRequired,
		},
		{
			name:     "unknown persona",
			setup:    seedUncleFu,
			args:     []string{"delete", "ghost", "--yes", "--json"},
			wantCode: codeNotFound,
		},
		{
			name: "last persona on disk",
			setup: func(t *testing.T) {
				t.Helper()
				// Remove the seeded builtin so exactly one profile remains.
				if err := os.RemoveAll(filepath.Join(persona.Dir(), "samantha")); err != nil {
					t.Fatal(err)
				}
				seedUncleFu(t)
				if err := config.SetAndSave("active_persona", "uncle-fu"); err != nil {
					t.Fatal(err)
				}
			},
			args:     []string{"delete", "uncle-fu", "--yes", "--json"},
			wantCode: codeLastPersona,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			personaEnv(t)
			tc.setup(t)

			out, err := runPersona(t, tc.args...)
			if err == nil {
				t.Fatalf("persona delete error = nil, want %s (out %s)", tc.wantCode, out)
			}
			code, msg, _ := decodeErrorJSON(t, out)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q (error %q)", code, tc.wantCode, msg)
			}
		})
	}
}

// A refused delete must not have touched the disk on the way to refusing.
func TestPersonaDeleteRefusalLeavesFiles(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	if _, err := runPersona(t, "delete", "uncle-fu", "--json"); err == nil {
		t.Fatal("persona delete error = nil, want confirm_required")
	}
	if _, err := persona.Load("uncle-fu"); err != nil {
		t.Fatalf("persona gone after a refused delete: %v", err)
	}
}

func TestPersonaDeleteRemovesDirAndOwnedPrompt(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	promptPath := filepath.Join(config.PromptsDir(), "persona", "uncle-fu.yaml")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("seeded prompt missing: %v", err)
	}

	out, err := runPersona(t, "delete", "uncle-fu", "--yes", "--json")
	if err != nil {
		t.Fatalf("persona delete error = %v (out %s)", err, out)
	}
	got := decodeDeleteResult(t, out)
	if got.Deleted != "uncle-fu" {
		t.Fatalf("deleted = %q", got.Deleted)
	}
	if len(got.Removed) != 2 {
		t.Fatalf("removed = %v, want the persona dir and its prompt", got.Removed)
	}
	if len(got.Kept) != 0 {
		t.Fatalf("kept = %v, want nothing kept", got.Kept)
	}
	if _, err := os.Stat(filepath.Join(persona.Dir(), "uncle-fu")); !os.IsNotExist(err) {
		t.Fatalf("persona dir still present: %v", err)
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("owned prompt still present: %v", err)
	}
}

// A prompt document another persona owns is a shared reference: removing it
// would break that persona, so it is kept and reported.
func TestPersonaDeleteKeepsSharedPrompt(t *testing.T) {
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

	out, err := runPersona(t, "delete", "borrower", "--yes", "--json")
	if err != nil {
		t.Fatalf("persona delete error = %v (out %s)", err, out)
	}
	got := decodeDeleteResult(t, out)
	if len(got.Kept) != 1 || got.Kept[0] != shared {
		t.Fatalf("kept = %v, want [%s]", got.Kept, shared)
	}
	for _, path := range got.Removed {
		if path == shared {
			t.Fatalf("removed the shared prompt %s", shared)
		}
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared prompt gone: %v", err)
	}
}

// Healing must go through Use: active_persona alone leaves agent_name and the
// voice keys naming a persona that no longer exists.
func TestPersonaDeleteHealsActivePersonaThroughUse(t *testing.T) {
	dir := personaEnv(t)
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "uncle-fu", DisplayName: "Uncle Fu",
		TTS:     persona.TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"},
		Prompts: persona.PromptRefs{Persona: "uncle-fu"},
	})
	if err := persona.WriteSystemPrompt("uncle-fu", "You are Uncle Fu."); err != nil {
		t.Fatal(err)
	}
	// Persist Uncle Fu's whole stack, so healing has to move every key back
	// and not just active_persona.
	for key, value := range map[string]any{
		"active_persona": "uncle-fu",
		"agent_name":     "Uncle Fu",
		"persona":        "uncle-fu",
		"tts_provider":   "qwen3-tts",
		"qwen_tts_voice": "Uncle_Fu",
	} {
		if err := config.SetAndSave(key, value); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runPersona(t, "delete", "uncle-fu", "--yes", "--json")
	if err != nil {
		t.Fatalf("persona delete error = %v (out %s)", err, out)
	}
	got := decodeDeleteResult(t, out)
	if !got.Reactivated || got.ActivePersona != "samantha" {
		t.Fatalf("result = %+v, want samantha re-activated", got)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	saved := string(raw)
	for _, want := range []string{"active_persona: samantha", "agent_name: Samantha", "persona: samantha", "tts_provider: kokoro", "tts_voice: af_heart"} {
		if !strings.Contains(saved, want) {
			t.Errorf("config.yaml missing %q:\n%s", want, saved)
		}
	}
}

// Deleting a persona that is not the active one leaves the active id alone.
func TestPersonaDeleteInactiveKeepsActive(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "delete", "uncle-fu", "--yes", "--json")
	if err != nil {
		t.Fatalf("persona delete error = %v (out %s)", err, out)
	}
	got := decodeDeleteResult(t, out)
	if got.Reactivated {
		t.Fatal("reactivated = true for a non-active persona")
	}
	if got.ActivePersona != "samantha" {
		t.Fatalf("active_persona = %q, want samantha", got.ActivePersona)
	}
}

// Confirmation is about a real persona: an unknown id is not_found even
// without --yes, so a UI never asks the user to confirm a no-op.
func TestPersonaDeleteUnknownBeatsConfirmation(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)

	out, err := runPersona(t, "delete", "ghost", "--json")
	if err == nil {
		t.Fatalf("persona delete error = nil, want not_found (out %s)", out)
	}
	code, msg, _ := decodeErrorJSON(t, out)
	if code != codeNotFound {
		t.Fatalf("code = %q, want %q (error %q)", code, codeNotFound, msg)
	}
}
