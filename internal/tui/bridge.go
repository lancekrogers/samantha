package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
)

// defaultBridgeCapacity bounds the bridge queue. Display state is droppable;
// stalling the pipeline is not.
const defaultBridgeCapacity = 256

// busEventMsg delivers one bus event into the Bubble Tea update loop.
type busEventMsg struct {
	event events.Event
}

// eventBridge translates the bus's synchronous push model into Bubble Tea's
// pull-per-Update model: handlers forward events into a bounded channel and a
// re-arming Cmd drains it.
type eventBridge struct {
	ch chan tea.Msg
}

func newEventBridge(capacity int) *eventBridge {
	if capacity <= 0 {
		capacity = defaultBridgeCapacity
	}
	return &eventBridge{ch: make(chan tea.Msg, capacity)}
}

// attach subscribes the bridge only to events the conversation view renders.
// High-volume intermediate events (segments, stream chunks, voice frames)
// would otherwise fill the drop-oldest queue and risk losing durable
// transcript events (UserInput, ResponseReady) under pressure.
func (b *eventBridge) attach(bus *events.Bus) {
	forward[events.STTPhase](b, bus)
	forward[events.UserInput](b, bus)
	forward[events.TranscriptPartial](b, bus)
	forward[events.ThinkingStarted](b, bus)
	forward[events.TurnMetrics](b, bus)
	forward[events.GeneratingVoice](b, bus)
	forward[events.SpeakingStarted](b, bus)
	forward[events.SpeakingComplete](b, bus)
	forward[events.SpeakingInterrupted](b, bus)
	forward[events.TurnInterrupted](b, bus)
	forward[events.ResponseReady](b, bus)
	forward[events.ConversationCleared](b, bus)
	forward[events.Error](b, bus)
	forward[events.Info](b, bus)
}

func forward[T events.Event](b *eventBridge, bus *events.Bus) {
	events.Subscribe(bus, func(e T) { b.send(busEventMsg{event: e}) })
}

// send never blocks: Bus.Emit runs handlers synchronously on the emitting
// pipeline goroutine, so back-pressure here would stall a turn mid-flight.
// When the channel is full the oldest message is dropped to make room.
func (b *eventBridge) send(msg tea.Msg) {
	for {
		select {
		case b.ch <- msg:
			return
		default:
		}
		select {
		case <-b.ch:
		default:
		}
	}
}

// wait blocks on the next bridged message; Update re-issues it after every
// delivery so the model keeps draining the bus.
func (b *eventBridge) wait() tea.Cmd {
	return func() tea.Msg { return <-b.ch }
}
