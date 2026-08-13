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
	if ack != (netapi.PersonaAck{ID: "pirate", DisplayName: "Captain", PromptHash: "pirate"}) {
		t.Fatalf("ack = %+v", ack)
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
