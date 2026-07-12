package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/pipeline"
)

// ConversationRuntime is a live pipeline prepared for the conversation
// screen. The TUI owns its lifecycle from the moment the builder returns it:
// Cleanup runs exactly once, after the program exits and in-flight turns
// have drained.
type ConversationRuntime struct {
	Pipeline *pipeline.Pipeline
	Bus      *events.Bus
	Voice    bool         // STT is configured; voice turns may run
	Seed     []brain.Turn // resumed history to pre-populate the viewport
	Cleanup  func()       // tears down pipeline resources and saves the session
}

// RuntimeBuilder constructs the runtime when the user enters the
// conversation screen (D2: the mic goes hot here, not in the launcher).
// Asset download progress is reported through progress and rendered
// in-screen.
type RuntimeBuilder func(ctx context.Context, progress func(name string, pct float64)) (*ConversationRuntime, error)

// assetProgressMsg carries EnsureRuntimeAssets progress into the update loop.
type assetProgressMsg struct {
	name string
	pct  float64
}

// progressClosedMsg tells the update loop to stop draining the progress feed.
type progressClosedMsg struct{}

// runtimeReadyMsg delivers the built runtime (or the fatal build error).
type runtimeReadyMsg struct {
	rt  *ConversationRuntime
	err error
}

// buildRuntime runs the builder off the update loop, streaming progress
// through the feed so the conversation screen can render it.
func buildRuntime(build RuntimeBuilder, ctx context.Context, feed *eventBridge) tea.Cmd {
	return func() tea.Msg {
		rt, err := build(ctx, func(name string, pct float64) {
			feed.send(assetProgressMsg{name: name, pct: pct})
		})
		feed.send(progressClosedMsg{})
		return runtimeReadyMsg{rt: rt, err: err}
	}
}

func formatAssetProgress(msg assetProgressMsg) string {
	if msg.pct <= 0 {
		return fmt.Sprintf("Downloading %s...", msg.name)
	}
	return fmt.Sprintf("Downloading %s: %d%%", msg.name, int(msg.pct))
}

// drainTimeout caps how long shutdown waits for an in-flight turn after its
// context is canceled, mirroring cmd/samantha's forceQuitTimeout.
const drainTimeout = 3 * time.Second

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
