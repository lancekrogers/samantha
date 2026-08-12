package voiceagent

import (
	"context"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// Error cases first, per repo standards.

func TestNewRejectsMissingRequirements(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"no config", Options{Events: events.NewBus()}, "Config is required"},
		{"no events", Options{Config: &config.Config{}}, "Events is required"},
		{"neither", Options{}, "Config is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, cleanup, err := New(context.Background(), tt.opts)
			if err == nil {
				t.Fatal("New succeeded with an incomplete Options")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if agent != nil || cleanup != nil {
				t.Error("a failed New must return no agent and no cleanup to call")
			}
		})
	}
}

// A provider that fails to construct must not leak the resources acquired before
// it. This is what the reverse-order cleanup stack exists for, and the reason
// every error path in New runs it before returning.
func TestNewRunsCleanupWhenAProviderFails(t *testing.T) {
	agent, cleanup, err := New(context.Background(), Options{
		Config:   &config.Config{BrainProvider: "nonexistent-provider-xyz"},
		Events:   events.NewBus(),
		TextOnly: true,
		Silent:   true,
	})
	if err == nil {
		t.Fatalf("New succeeded with an unknown brain provider; agent=%v", agent)
	}
	if cleanup != nil {
		t.Error("a failed New must not hand back a cleanup — it has already run its own")
	}
}

// The embedding contract: every provider can be supplied by the host, and when
// all of them are, New must not touch a microphone, a speaker or a model file.
// If this test ever needs hardware, the surface has stopped being embeddable.
func TestNewAcceptsFullyInjectedProviders(t *testing.T) {
	bus := events.NewBus()
	fakeBrain := &stubBrain{}
	fakeTTS := &stubTTS{}
	fakePlayer := &stubPlayer{}

	agent, cleanup, err := New(context.Background(), Options{
		Config:   &config.Config{AgentName: "Test", VoiceToolsEnabled: true},
		Events:   bus,
		Brain:    fakeBrain,
		TTS:      fakeTTS,
		Player:   fakePlayer,
		TextOnly: true, // no capture device
	})
	if err != nil {
		t.Fatalf("New with injected providers: %v", err)
	}
	defer cleanup()

	if agent.Pipeline == nil {
		t.Fatal("agent has no pipeline")
	}
	if agent.Brain != brain.Provider(fakeBrain) {
		t.Error("injected Brain was not used")
	}
	if agent.Player != audio.Engine(fakePlayer) {
		t.Error("injected Player was not used")
	}
	if !agent.HasTTS() {
		t.Error("injected TTS was not installed")
	}
	if !agent.VoiceToolsEnabled {
		t.Error("Config.VoiceToolsEnabled did not reach the pipeline")
	}
}

// Silent mode must not construct a player at all, so a host that only wants text
// turns never opens an audio device.
func TestSilentModeBuildsNoPlayer(t *testing.T) {
	agent, cleanup, err := New(context.Background(), Options{
		Config:   &config.Config{AgentName: "Test"},
		Events:   events.NewBus(),
		Brain:    &stubBrain{},
		TextOnly: true,
		Silent:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	if agent.Player != nil {
		t.Error("silent mode built a player")
	}
	if agent.HasTTS() {
		t.Error("silent mode installed a TTS provider")
	}
}

// The cleanup must be safe to call, and calling it must not panic on a partially
// configured agent — hosts defer it immediately after New.
func TestCleanupIsSafeOnAMinimalAgent(t *testing.T) {
	_, cleanup, err := New(context.Background(), Options{
		Config:   &config.Config{AgentName: "Test"},
		Events:   events.NewBus(),
		Brain:    &stubBrain{},
		TextOnly: true,
		Silent:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanup()
}

// Logf receives non-fatal setup problems rather than them going to stderr
// unconditionally. A nil Logf must be safe.
func TestNilLogfIsSafe(t *testing.T) {
	_, cleanup, err := New(context.Background(), Options{
		Config:   &config.Config{AgentName: "Test"},
		Events:   events.NewBus(),
		Brain:    &stubBrain{},
		TextOnly: true,
		Silent:   true,
		Logf:     nil,
	})
	if err != nil {
		t.Fatalf("New with nil Logf: %v", err)
	}
	cleanup()
}

// --- stubs -----------------------------------------------------------------

type stubBrain struct{ history []brain.Turn }

func (s *stubBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func (s *stubBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "ok", nil
}
func (s *stubBrain) ClearHistory()                  { s.history = nil }
func (s *stubBrain) History() []brain.Turn          { return s.history }
func (s *stubBrain) LoadHistory(turns []brain.Turn) { s.history = turns }

type stubTTS struct{}

func (s *stubTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream(ctx)
	stream.Close()
	return stream, nil
}
func (s *stubTTS) Available() bool                       { return true }
func (s *stubTTS) ListVoices(string, string) []tts.Voice { return nil }

type stubPlayer struct{}

func (s *stubPlayer) PlayStream(context.Context, *audio.PCMStream) (*audio.Playback, error) {
	return nil, nil
}
func (s *stubPlayer) Stop()           {}
func (s *stubPlayer) IsPlaying() bool { return false }
func (s *stubPlayer) Close() error    { return nil }
