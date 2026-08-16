package voiceagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
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

type stubCapture struct{}

func (*stubCapture) Subscribe(int) (int, <-chan []float32) { return 0, make(chan []float32) }
func (*stubCapture) Unsubscribe(int)                       {}
func (*stubCapture) Reset()                                {}

type stubSTT struct{}

func (*stubSTT) Start(context.Context) (stt.Session, error) { return nil, nil }
func (*stubSTT) Available() bool                            { return true }

// --- event stream ----------------------------------------------------------

// The bus is synchronous: its handlers run inline on the pipeline's goroutine.
// A slow consumer must therefore lose events rather than stall speech, and must
// be able to tell that it did.
func TestEventStreamDropsRatherThanBlocking(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	const buffer = 4
	stream := agent.EventStream(buffer)
	defer stream.Close()

	// Emit well past the buffer without reading a single event. If the adapter
	// blocked, this would deadlock and the test would time out.
	const emitted = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range emitted {
			agent.Pipeline.Events.Emit(events.Error{Stage: "test", Message: "x"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked — a full EventStream must drop, not stall the pipeline")
	}

	if got := stream.Dropped(); got == 0 {
		t.Error("Dropped() = 0 after overflowing the buffer; a silent drop policy is " +
			"indistinguishable from a bug")
	}
	if got := len(stream.C()); got > buffer {
		t.Errorf("buffered %d events, want at most %d", got, buffer)
	}
}

func TestEventStreamDeliversWhenKeepingUp(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	stream := agent.EventStream(16)
	defer stream.Close()

	agent.Pipeline.Events.Emit(events.Error{Stage: "test", Message: "hello"})
	select {
	case e := <-stream.C():
		if got, ok := e.(events.Error); !ok || got.Message != "hello" {
			t.Errorf("received %#v, want the published Error event", e)
		}
	case <-time.After(time.Second):
		t.Fatal("published event never arrived")
	}
	if got := stream.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d with a consumer keeping up, want 0", got)
	}
}

// Close must unsubscribe. A stream that keeps receiving after Close would panic
// on send-to-closed-channel the moment the next event fires.
func TestEventStreamCloseUnsubscribes(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	stream := agent.EventStream(8)
	stream.Close()
	stream.Close() // must be idempotent

	// Publishing after Close must not panic.
	agent.Pipeline.Events.Emit(events.Error{Stage: "test", Message: "after close"})

	if _, open := <-stream.C(); open {
		t.Error("channel should be closed and drained after Close")
	}
}

// A zero or negative buffer must not produce an unbuffered channel, which would
// drop everything except perfectly-timed reads.
func TestEventStreamRejectsUnbufferedRequest(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	for _, buffer := range []int{0, -1} {
		stream := agent.EventStream(buffer)
		if cap(stream.C()) == 0 {
			t.Errorf("EventStream(%d) produced an unbuffered channel", buffer)
		}
		stream.Close()
	}
}

// --- interrupt -------------------------------------------------------------

// Both are ordinary host states, not errors: a hotkey pressed before anything is
// running, and a hotkey pressed twice.
func TestInterruptIsSafeWithNoTurnAndWhenRepeated(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	agent.Interrupt()
	agent.Interrupt()
}

func TestSendTextClearsOnlyItsOwnCancel(t *testing.T) {
	agent, cleanup := textAgent(t)
	defer cleanup()

	if err := agent.SendText(context.Background(), "first"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	// After a completed turn there is nothing in flight, so Interrupt is a no-op
	// rather than a panic or a stale cancel firing into the next turn.
	agent.Interrupt()

	if err := agent.SendText(context.Background(), "second"); err != nil {
		t.Fatalf("second SendText: %v", err)
	}
}

func textAgent(t *testing.T) (*Agent, func()) {
	t.Helper()
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
	return agent, cleanup
}

// A field that is accepted and then ignored is worse than an absent one. These
// pin that the two stage-2 options actually reach the thing they configure.
// Env and PromptsDir only reach a brain New builds from Config. With an
// injected Brain they would be silently ignored, so New must refuse them —
// a constructor that accepts an option and discards it is how this library's
// earliest bugs looked.
func TestNewRejectsDeadOptionsWithInjectedBrain(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			"env with injected brain",
			Options{
				Config: &config.Config{AgentName: "Test"},
				Events: events.NewBus(), Brain: &stubBrain{},
				TextOnly: true, Silent: true,
				Env: brain.Env{User: "svc"},
			},
			"Options.Env",
		},
		{
			"prompts dir with injected brain",
			Options{
				Config: &config.Config{AgentName: "Test"},
				Events: events.NewBus(), Brain: &stubBrain{},
				TextOnly: true, Silent: true,
				PromptsDir: "/tmp/some-embedder-prompts",
			},
			"Options.PromptsDir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, cleanup, err := New(context.Background(), tt.opts)
			if err == nil {
				t.Fatal("New accepted an option an injected Brain can never see")
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

func TestOptionsEnvReachesBrainConstructedByNew(t *testing.T) {
	var systemPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"test-model"}]}`)
		case "/api/chat":
			body, _ := io.ReadAll(r.Body)
			systemPrompt = string(body)
			_, _ = io.WriteString(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		AgentName:                 "Test",
		BrainProvider:             "ollama",
		OllamaModel:               "test-model",
		OllamaHost:                srv.URL,
		EnvironmentContextEnabled: true,
	}
	agent, cleanup, err := New(context.Background(), Options{
		Config: cfg, Events: events.NewBus(), TextOnly: true, Silent: true,
		Env:        brain.Env{User: "svc-account", Hostname: "prod-1", OS: "linux/amd64"},
		PromptsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	if err := agent.SendText(context.Background(), "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	for _, want := range []string{"User: svc-account", "Hostname: prod-1", "OS: linux/amd64"} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("Options.Env did not reach New's brain: want %q in request %s", want, systemPrompt)
		}
	}
	if cfg.RuntimeEnvUser != "" || cfg.RuntimeEnvHostname != "" || cfg.RuntimeEnvOS != "" {
		t.Fatal("New mutated the caller's config with runtime environment values")
	}
	// The caller's config must not be rewritten underneath it: a host reusing
	// one *Config across agents would otherwise find it mutated.
	if cfg.PromptsDir != "" {
		t.Errorf("New mutated the caller's Config.PromptsDir to %q", cfg.PromptsDir)
	}
}

func TestOptionsCaptureIsInstalledWithoutOpeningLocalDevice(t *testing.T) {
	capture := &stubCapture{}
	agent, cleanup, err := New(context.Background(), Options{
		Config: &config.Config{AgentName: "Test", VoiceFrontendEnabled: true},
		Events: events.NewBus(), Brain: &stubBrain{}, Silent: true,
		Capture: capture, STT: &stubSTT{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	if agent.Capture != capture {
		t.Fatal("Options.Capture was ignored")
	}
}
