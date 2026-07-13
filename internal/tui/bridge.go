package tui

import (
	"sync"

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
	// mu serializes full-queue eviction rebuilds so concurrent Emit handlers
	// cannot interleave drain/reinsert and drop durable transcript events.
	mu sync.Mutex
}

func newEventBridge(capacity int) *eventBridge {
	if capacity <= 0 {
		capacity = defaultBridgeCapacity
	}
	return &eventBridge{ch: make(chan tea.Msg, capacity)}
}

// attach subscribes the bridge only to events the conversation and activity
// panes render. Raw model chunks, audio frames, and per-segment voice events
// stay excluded: a multi-sentence reply can emit several segment events each,
// and under UI lag those would fill the drop-oldest queue and displace durable
// transcript events (UserInput / ResponseReady).
func (b *eventBridge) attach(bus *events.Bus) {
	forward[events.STTPhase](b, bus)
	forward[events.UserInput](b, bus)
	forward[events.TranscriptPartial](b, bus)
	forward[events.ThinkingStarted](b, bus)
	forward[events.ResponseStreamingStarted](b, bus)
	forward[events.ThinkingComplete](b, bus)
	forward[events.TurnMetrics](b, bus)
	// GeneratingVoice / Speaking* are once-per-playback milestones; segment
	// ready/generated events are intentionally not bridged (see above).
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

// isDurableBridgeMsg reports events the transcript must not lose under pressure.
func isDurableBridgeMsg(msg tea.Msg) bool {
	be, ok := msg.(busEventMsg)
	if !ok {
		return false
	}
	switch be.event.(type) {
	case events.UserInput, events.ResponseReady, events.ConversationCleared, events.Error:
		return true
	default:
		return false
	}
}

// send never blocks: Bus.Emit runs handlers synchronously on the emitting
// pipeline goroutine, so back-pressure here would stall a turn mid-flight.
// When the channel is full, non-durable activity is dropped first so durable
// transcript events (UserInput / ResponseReady) survive multi-segment floods.
func (b *eventBridge) send(msg tea.Msg) {
	select {
	case b.ch <- msg:
		return
	default:
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Another sender may have freed a slot while we waited for the lock.
	select {
	case b.ch <- msg:
		return
	default:
	}

	// Drain, append the new message, drop oldest non-durable first until the
	// queue fits capacity, then reinsert. Capacity is small (≤256), so the
	// rebuild is cheap compared to stalling synthesis.
	q := make([]tea.Msg, 0, cap(b.ch)+1)
	for {
		select {
		case m := <-b.ch:
			q = append(q, m)
		default:
			goto drained
		}
	}
drained:
	q = append(q, msg)
	q = fitBridgeQueue(q, cap(b.ch))
	for _, m := range q {
		select {
		case b.ch <- m:
		default:
			// Should be impossible after fitBridgeQueue; drop rather than block.
		}
	}
}

// fitBridgeQueue keeps at most capacity messages, dropping oldest non-durable
// activity first. If only durable events remain, the oldest durable is dropped.
func fitBridgeQueue(q []tea.Msg, capacity int) []tea.Msg {
	if capacity <= 0 {
		return nil
	}
	for len(q) > capacity {
		drop := -1
		for i, m := range q {
			if !isDurableBridgeMsg(m) {
				drop = i
				break
			}
		}
		if drop < 0 {
			q = q[1:]
			continue
		}
		q = append(q[:drop], q[drop+1:]...)
	}
	return q
}

// wait blocks on the next bridged message; Update re-issues it after every
// delivery so the model keeps draining the bus.
func (b *eventBridge) wait() tea.Cmd {
	return func() tea.Msg { return <-b.ch }
}
