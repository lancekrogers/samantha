package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

type stubBrain struct {
	cleared bool
}

func (b *stubBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("not implemented")
}
func (b *stubBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (b *stubBrain) ClearHistory()            { b.cleared = true }
func (b *stubBrain) History() []brain.Turn    { return nil }
func (b *stubBrain) LoadHistory([]brain.Turn) {}

func wiredApp(build RuntimeBuilder) App {
	app := NewApp(&config.Config{})
	app.builder = build
	app.runCtx = context.Background()
	app.wg = &sync.WaitGroup{}
	app.progress = newEventBridge(16)
	app.slot = &runtimeSlot{}
	return app
}

func fakeRuntime() *ConversationRuntime {
	return &ConversationRuntime{
		Pipeline: &pipeline.Pipeline{Brain: &stubBrain{}},
		Bus:      events.NewBus(),
		Voice:    false,
	}
}

// "Start conversation" switches screens in place and kicks off the build —
// no tea.Quit hop (D2).
func TestStartPipelineEntersConversationScreen(t *testing.T) {
	app := wiredApp(func(ctx context.Context, progress func(string, float64), _, _ string) (*ConversationRuntime, error) {
		return fakeRuntime(), nil
	})

	model, cmd := app.Update(startPipelineMsg{})
	app = model.(App)
	if app.screen != screenConversation {
		t.Fatalf("screen = %d, want conversation", app.screen)
	}
	if cmd == nil {
		t.Fatal("entering conversation did not start the runtime build")
	}
	if app.quitting {
		t.Fatal("startPipelineMsg must not quit the program anymore")
	}
}

// The builder runs off the update loop; its progress lands as in-screen
// status updates and the finished runtime wires the conversation model.
func TestRuntimeBuildProgressAndReady(t *testing.T) {
	rt := fakeRuntime()
	build := func(ctx context.Context, progress func(string, float64), _, _ string) (*ConversationRuntime, error) {
		progress("kokoro-v1", 0)
		progress("kokoro-v1", 42)
		return rt, nil
	}
	app := wiredApp(build)
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
	ready, ok := msg.(runtimeReadyMsg)
	if !ok || ready.err != nil {
		t.Fatalf("buildRuntime returned %#v, want clean runtimeReadyMsg", msg)
	}

	// Drain the two progress updates the builder emitted.
	for range 2 {
		model, _ = app.Update(app.progress.wait()())
		app = model.(App)
	}
	if !strings.Contains(app.conversation.status, "42%") {
		t.Errorf("progress status = %q, want it to show 42%%", app.conversation.status)
	}

	model, cmd := app.Update(ready)
	app = model.(App)
	if app.runtime != rt {
		t.Fatal("runtime not stored on the app")
	}
	if cmd == nil {
		t.Fatal("ready runtime did not arm the conversation (bridge drain missing)")
	}
	if app.conversation.deps.runner == nil {
		t.Fatal("conversation model not wired to the pipeline")
	}
}

func TestRuntimeBuildReceivesSelectedSession(t *testing.T) {
	var selected string
	app := wiredApp(func(_ context.Context, _ func(string, float64), sessionID, _ string) (*ConversationRuntime, error) {
		selected = sessionID
		return fakeRuntime(), nil
	})
	model, _ := app.Update(startPipelineMsg{sessionID: "saved-session"})
	app = model.(App)
	_ = buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "saved-session", "")()
	if selected != "saved-session" {
		t.Fatalf("builder session = %q, want saved-session", selected)
	}
}

func TestRuntimeBuildReceivesSelectedPersona(t *testing.T) {
	var selected string
	app := wiredApp(func(_ context.Context, _ func(string, float64), _, personaID string) (*ConversationRuntime, error) {
		selected = personaID
		return fakeRuntime(), nil
	})
	model, _ := app.Update(startPipelineMsg{personaID: "research-buddy"})
	app = model.(App)
	_ = buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "research-buddy")()
	if selected != "research-buddy" {
		t.Fatalf("builder persona = %q, want research-buddy", selected)
	}
}

// The conversation renders the binding's agent name for its whole lifetime —
// a persona switch after start must not relabel an in-flight session.
func TestRuntimeReadyBindsAgentName(t *testing.T) {
	rt := fakeRuntime()
	rt.AgentName = "Research Buddy"
	app := wiredApp(func(context.Context, func(string, float64), string, string) (*ConversationRuntime, error) {
		return rt, nil
	})
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
	model, _ = app.Update(msg)
	app = model.(App)

	if app.conversation.agentName != "Research Buddy" {
		t.Fatalf("conversation agentName = %q, want binding identity", app.conversation.agentName)
	}
}

func TestRuntimeBuildFailureQuitsWithError(t *testing.T) {
	build := func(ctx context.Context, progress func(string, float64), _, _ string) (*ConversationRuntime, error) {
		return nil, errors.New("no assets, no pipeline")
	}
	app := wiredApp(build)
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
	model, cmd := app.Update(msg)
	app = model.(App)

	if app.fatalErr == nil {
		t.Fatal("build failure not recorded as fatal error")
	}
	if cmd == nil {
		t.Fatal("build failure must quit the program")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("build failure cmd is not tea.Quit")
	}
}

// If the program context is canceled while the builder is still running, the
// finished runtime must be cleaned up by the slot — not leaked because the
// ready message never reaches Update.
func TestRuntimeBuildCancelsCleanupViaSlot(t *testing.T) {
	cleaned := false
	rt := fakeRuntime()
	rt.Cleanup = func() { cleaned = true }

	ctx, cancel := context.WithCancel(context.Background())
	app := wiredApp(func(context.Context, func(string, float64), string, string) (*ConversationRuntime, error) {
		return rt, nil
	})
	app.runCtx = ctx
	cancel() // simulate quit-during-build before the Cmd finishes

	msg := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
	ready, ok := msg.(runtimeReadyMsg)
	if !ok {
		t.Fatalf("buildRuntime returned %#v", msg)
	}
	if ready.err == nil {
		t.Fatal("expected canceled context error when building after cancel")
	}
	if !cleaned {
		t.Fatal("runtime Cleanup not called when ctx was canceled after build")
	}
}

// Resumed sessions land in the conversation screen with the viewport seeded
// from persisted turns: normalized roles map through the same rendering
// functions live events use, and non-user/assistant roles are dropped.
func TestRuntimeSeedPopulatesViewport(t *testing.T) {
	rt := fakeRuntime()
	rt.Seed = []brain.Turn{
		{Role: "user", Content: "what did we decide yesterday"},
		{Role: "assistant", Content: "you locked D1 through D3"},
		{Role: "tool", Content: "tool output that must not render"},
	}
	app := wiredApp(func(ctx context.Context, progress func(string, float64), _, _ string) (*ConversationRuntime, error) {
		return rt, nil
	})
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)
	model, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
	model, _ = app.Update(msg)
	app = model.(App)

	view := app.conversation.View()
	for _, want := range []string{"what did we decide yesterday", "you locked D1 through D3"} {
		if !strings.Contains(view, want) {
			t.Errorf("seeded viewport missing %q", want)
		}
	}
	if strings.Contains(view, "tool output") {
		t.Error("tool turn rendered; seeding must drop non-user/assistant roles")
	}
}

// resume/continue start the program directly in the conversation screen.
func TestInitStartsConversationWhenResuming(t *testing.T) {
	app := wiredApp(func(ctx context.Context, progress func(string, float64), _, _ string) (*ConversationRuntime, error) {
		return fakeRuntime(), nil
	})
	app.startInConversation = true

	cmd := app.Init()
	if cmd == nil {
		t.Fatal("Init returned no cmd despite startInConversation")
	}
	if _, ok := cmd().(startPipelineMsg); !ok {
		t.Fatal("Init cmd did not produce startPipelineMsg")
	}

	if NewApp(&config.Config{}).Init() != nil {
		t.Fatal("launcher entry must keep a nil Init")
	}
}

// Without runtime wiring (unit-test Apps), startPipelineMsg must be inert
// rather than panicking on a nil builder.
func TestStartPipelineWithoutBuilderIsInert(t *testing.T) {
	app := App{cfg: &config.Config{}}
	model, cmd := app.Update(startPipelineMsg{})
	if cmd != nil || model.(App).screen == screenConversation {
		t.Fatal("nil-builder app must ignore startPipelineMsg")
	}
}
