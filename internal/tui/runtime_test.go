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
func (b *stubBrain) ThinkFull(context.Context, string) (string, error) { return "", nil }
func (b *stubBrain) ClearHistory()                                     { b.cleared = true }
func (b *stubBrain) History() []brain.Turn                             { return nil }
func (b *stubBrain) LoadHistory([]brain.Turn)                          {}

func wiredApp(build RuntimeBuilder) App {
	app := NewApp(&config.Config{})
	app.builder = build
	app.runCtx = context.Background()
	app.wg = &sync.WaitGroup{}
	app.progress = newEventBridge(16)
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
	app := wiredApp(func(ctx context.Context, progress func(string, float64)) (*ConversationRuntime, error) {
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
	build := func(ctx context.Context, progress func(string, float64)) (*ConversationRuntime, error) {
		progress("kokoro-v1", 0)
		progress("kokoro-v1", 42)
		return rt, nil
	}
	app := wiredApp(build)
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress)()
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

func TestRuntimeBuildFailureQuitsWithError(t *testing.T) {
	build := func(ctx context.Context, progress func(string, float64)) (*ConversationRuntime, error) {
		return nil, errors.New("no assets, no pipeline")
	}
	app := wiredApp(build)
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress)()
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
	app := wiredApp(func(ctx context.Context, progress func(string, float64)) (*ConversationRuntime, error) {
		return rt, nil
	})
	model, _ := app.Update(startPipelineMsg{})
	app = model.(App)
	model, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = model.(App)

	msg := buildRuntime(app.builder, app.runCtx, app.progress)()
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
	app := wiredApp(func(ctx context.Context, progress func(string, float64)) (*ConversationRuntime, error) {
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
