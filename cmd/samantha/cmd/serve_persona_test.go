//go:build !integration

package cmd

import (
	"context"
	"testing"

	"github.com/lancekrogers/samantha/internal/netapi"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/pipeline"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
)

type servePersonaTestBrain struct {
	history []brain.Turn
}

func (*servePersonaTestBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (*servePersonaTestBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (*servePersonaTestBrain) ClearHistory() {}
func (b *servePersonaTestBrain) History() []brain.Turn {
	return append([]brain.Turn(nil), b.history...)
}
func (b *servePersonaTestBrain) LoadHistory(turns []brain.Turn) {
	b.history = append([]brain.Turn(nil), turns...)
}

func TestServePersonaSwitcherReplacesBrainTTSAndSession(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	profile := &persona.Profile{
		Schema:      persona.Schema,
		ID:          "pirate",
		DisplayName: "Captain",
		Brain:       persona.Brain{Provider: "ollama", Model: "pirate-model"},
		TTS:         persona.TTS{Provider: "kokoro", Voice: "pm-pirate"},
		Prompts:     persona.PromptRefs{Persona: "pirate"},
	}
	if err := persona.Write(profile, false); err != nil {
		t.Fatal(err)
	}
	// The ack hashes the identity text, so the persona needs a real document.
	if err := persona.WriteSystemPrompt("pirate", "You are Captain, a pirate."); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ActivePersona: "samantha",
		AgentName:     "Samantha",
		Persona:       "samantha",
		BrainProvider: "ollama",
		OllamaModel:   "old-model",
		TTSProvider:   "kokoro",
		TTSVoice:      "old-voice",
	}
	oldBrain := &servePersonaTestBrain{history: []brain.Turn{{Role: "user", Content: "old turn"}}}
	newBrain := &servePersonaTestBrain{}
	p := &pipeline.Pipeline{Brain: oldBrain}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	ref := &sessionRef{sess: session.New(cfg.BrainProvider, cfg.OllamaModel)}
	oldSessionID := ref.sess.ID

	var brainCfg, ttsCfg config.Config
	switcher := &servePersonaSwitcher{
		cfg: cfg, pipeline: p, tts: manager, sessions: ref,
		newBrain: func(got *config.Config) (brain.Provider, error) {
			brainCfg = *got
			return newBrain, nil
		},
		newTTS: func(got *config.Config) (*voiceagent.TTSSet, error) {
			ttsCfg = *got
			return &voiceagent.TTSSet{}, nil
		},
	}

	ack, err := switcher.apply("pirate")
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	wantHash, err := persona.PromptHashFor("pirate")
	if err != nil {
		t.Fatal(err)
	}
	if ack != (netapi.PersonaAck{ID: "pirate", DisplayName: "Captain", PromptHash: wantHash}) {
		t.Fatalf("ack = %+v, want prompt_hash %q", ack, wantHash)
	}
	if p.Brain != newBrain {
		t.Fatal("pipeline brain was not replaced")
	}
	if brainCfg.OllamaModel != "pirate-model" || brainCfg.Persona != "pirate" {
		t.Fatalf("brain config = model %q persona %q", brainCfg.OllamaModel, brainCfg.Persona)
	}
	if ttsCfg.TTSProvider != "kokoro" || ttsCfg.TTSVoice != "pm-pirate" {
		t.Fatalf("TTS config = provider %q voice %q", ttsCfg.TTSProvider, ttsCfg.TTSVoice)
	}
	if cfg.ActivePersona != "pirate" || cfg.AgentName != "Captain" {
		t.Fatalf("live config = persona %q agent %q", cfg.ActivePersona, cfg.AgentName)
	}
	if ref.sess.ID == oldSessionID {
		t.Fatal("persona switch reused the previous identity's session")
	}
	if ref.sess.Provider != "ollama" || ref.sess.Model != "pirate-model" {
		t.Fatalf("new session = provider %q model %q", ref.sess.Provider, ref.sess.Model)
	}
}

// newSwitcherForTest builds a switcher whose brain and TTS construction are
// stubs, so a test exercises the persona resolution and ack, not the runtime.
func newSwitcherForTest(t *testing.T, cfg *config.Config) *servePersonaSwitcher {
	t.Helper()
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	return &servePersonaSwitcher{
		cfg:      cfg,
		pipeline: &pipeline.Pipeline{Brain: &servePersonaTestBrain{}},
		tts:      manager,
		sessions: &sessionRef{sess: session.New(cfg.BrainProvider, cfg.OllamaModel)},
		newBrain: func(*config.Config) (brain.Provider, error) { return &servePersonaTestBrain{}, nil },
		newTTS:   func(*config.Config) (*voiceagent.TTSSet, error) { return &voiceagent.TTSSet{}, nil },
	}
}

func writeAckTestPersona(t *testing.T, id, body string) {
	t.Helper()
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: id, DisplayName: id,
		TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: persona.PromptRefs{Persona: id},
	}, false); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		return
	}
	if err := persona.WriteSystemPrompt(id, body); err != nil {
		t.Fatal(err)
	}
}

// The field exists so a client can tell whether the model sees the prompt it
// just edited. A document name would never change and answer nothing.
func TestServePersonaAckHashTracksTheDocument(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "scribe", "You are Scribe, a careful note taker.")
	cfg := &config.Config{ActivePersona: "scribe", BrainProvider: "ollama", TTSProvider: "kokoro"}

	ack, err := newSwitcherForTest(t, cfg).apply("scribe")
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	if ack.PromptHash == "scribe" {
		t.Fatal("prompt_hash is the document name, not a hash")
	}
	if len(ack.PromptHash) != persona.PromptHashPrefix {
		t.Fatalf("prompt_hash = %q, want %d hex chars", ack.PromptHash, persona.PromptHashPrefix)
	}
	doc, err := persona.DescribePrompt(mustLoadPersona(t, "scribe"))
	if err != nil {
		t.Fatal(err)
	}
	if ack.PromptHash != doc.Hash {
		t.Fatalf("prompt_hash = %q, want the assembled document hash %q", ack.PromptHash, doc.Hash)
	}

	// Edit the body: the same persona must now ack a different hash.
	if err := persona.WriteSystemPrompt("scribe", "You are Scribe, and you answer in one line."); err != nil {
		t.Fatal(err)
	}
	edited, err := newSwitcherForTest(t, cfg).apply("scribe")
	if err != nil {
		t.Fatalf("apply() after edit error = %v", err)
	}
	if edited.PromptHash == ack.PromptHash {
		t.Fatalf("prompt_hash stayed %q after the document changed", edited.PromptHash)
	}
}

// An unresolvable document must not fail a switch whose brain and voice did
// install; the ack simply carries no hash.
func TestServePersonaAckHashEmptyWhenUnresolvable(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "ghostwriter", "")
	cfg := &config.Config{ActivePersona: "ghostwriter", BrainProvider: "ollama", TTSProvider: "kokoro"}

	ack, err := newSwitcherForTest(t, cfg).apply("ghostwriter")
	if err != nil {
		t.Fatalf("apply() error = %v, want the switch to succeed", err)
	}
	if ack.PromptHash != "" {
		t.Fatalf("prompt_hash = %q, want empty for an unresolvable document", ack.PromptHash)
	}
	if ack.ID != "ghostwriter" {
		t.Fatalf("ack = %+v", ack)
	}
}

func mustLoadPersona(t *testing.T, id string) *persona.Profile {
	t.Helper()
	p, err := persona.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
