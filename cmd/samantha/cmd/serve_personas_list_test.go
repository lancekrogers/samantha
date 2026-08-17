//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/pipeline"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

func findPersonaRow(t *testing.T, rows []netapi.PersonaSummary, id string) netapi.PersonaSummary {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row for %q in %+v", id, rows)
	return netapi.PersonaSummary{}
}

// A persona's empty fields mean "inherit", so the wire has to carry the values
// a turn would actually run with, not the profile's blanks.
func TestServePersonaListReportsEffectiveStack(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: "samantha", DisplayName: "Samantha", Builtin: true,
		Prompts: persona.PromptRefs{Persona: "samantha"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: "uncle-fu", DisplayName: "Uncle Fu",
		Brain:   persona.Brain{Provider: "ollama", Model: "qwen2.5:14b"},
		TTS:     persona.TTS{Provider: "qwen3-tts", Voice: "Uncle_Fu", Tier: "1.7b"},
		Prompts: persona.PromptRefs{Persona: "uncle-fu"},
	}, false); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ActivePersona: "samantha", AgentName: "Samantha", Persona: "samantha",
		BrainProvider: "ollama", OllamaModel: "app-default-model",
		TTSProvider: "kokoro", TTSVoice: "af_heart",
	}
	rows, err := servePersonaList(cfg)()
	if err != nil {
		t.Fatalf("servePersonaList() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}

	builtin := findPersonaRow(t, rows, "samantha")
	if !builtin.Builtin || !builtin.Active {
		t.Fatalf("samantha row = %+v, want builtin and active", builtin)
	}
	// Inherited from the app config, not reported as blank.
	if builtin.Brain.Model != "app-default-model" || builtin.TTS.Voice != "af_heart" {
		t.Fatalf("samantha stack = %+v / %+v, want the app defaults", builtin.Brain, builtin.TTS)
	}
	if builtin.TTS.Tier != "" {
		t.Fatalf("kokoro tier = %q, want empty — only qwen3-tts reads a tier", builtin.TTS.Tier)
	}

	uncle := findPersonaRow(t, rows, "uncle-fu")
	if uncle.Builtin || uncle.Active {
		t.Fatalf("uncle-fu row = %+v, want neither builtin nor active", uncle)
	}
	// Qwen keeps its voice in its own key; the row must follow the override.
	if uncle.TTS.Provider != "qwen3-tts" || uncle.TTS.Voice != "Uncle_Fu" || uncle.TTS.Tier != "1.7b" {
		t.Fatalf("uncle-fu tts = %+v", uncle.TTS)
	}
	if uncle.Brain.Model != "qwen2.5:14b" {
		t.Fatalf("uncle-fu brain = %+v, want its own model", uncle.Brain)
	}
}

// The whole point of the route: a runtime set_persona changes nothing on disk,
// so a list reading config.yaml would report the wrong persona for the rest of
// the session.
func TestServePersonaListActiveFollowsRuntimeSwitch(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("active_persona: samantha\nagent_name: Samantha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*persona.Profile{
		{
			Schema: persona.Schema, ID: "samantha", DisplayName: "Samantha", Builtin: true,
			TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: persona.PromptRefs{Persona: "samantha"},
		},
		{
			Schema: persona.Schema, ID: "pirate", DisplayName: "Captain",
			Brain: persona.Brain{Provider: "ollama", Model: "pirate-model"},
			TTS:   persona.TTS{Provider: "kokoro", Voice: "pm-pirate"}, Prompts: persona.PromptRefs{Persona: "pirate"},
		},
	} {
		if err := persona.Write(p, false); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		ActivePersona: "samantha", AgentName: "Samantha", Persona: "samantha",
		BrainProvider: "ollama", OllamaModel: "old-model",
		TTSProvider: "kokoro", TTSVoice: "af_heart",
	}
	list := servePersonaList(cfg)

	rows, err := list()
	if err != nil {
		t.Fatalf("servePersonaList() error = %v", err)
	}
	if !findPersonaRow(t, rows, "samantha").Active {
		t.Fatal("samantha is not active before the switch")
	}

	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	switcher := &servePersonaSwitcher{
		cfg:      cfg,
		pipeline: &pipeline.Pipeline{Brain: &servePersonaTestBrain{}},
		tts:      manager,
		sessions: &sessionRef{sess: session.New(cfg.BrainProvider, cfg.OllamaModel)},
		newBrain: func(*config.Config) (brain.Provider, error) { return &servePersonaTestBrain{}, nil },
		newTTS:   func(*config.Config) (*voiceagent.TTSSet, error) { return &voiceagent.TTSSet{}, nil },
	}
	if _, err := switcher.apply("pirate"); err != nil {
		t.Fatalf("apply() error = %v", err)
	}

	rows, err = list()
	if err != nil {
		t.Fatalf("servePersonaList() error = %v", err)
	}
	if !findPersonaRow(t, rows, "pirate").Active {
		t.Fatal("pirate is not active after a runtime set_persona")
	}
	if findPersonaRow(t, rows, "samantha").Active {
		t.Fatal("samantha still reports active after the switch")
	}

	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "active_persona: samantha") {
		t.Fatalf("config.yaml changed — a runtime switch must not persist:\n%s", saved)
	}
}
