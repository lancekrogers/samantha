package events

import (
	"fmt"
	"sync"
	"testing"
)

func TestEventTypeExposesWireNames(t *testing.T) {
	tests := []struct {
		event Event
		want  string
	}{
		{UserInput{}, "user_input"},
		{ResponseReady{}, "response_ready"},
		{TurnMetrics{}, "turn_metrics"},
		{TurnInterrupted{}, "turn_interrupted"},
		{ConversationCleared{}, "conversation_cleared"},
	}
	for _, tt := range tests {
		if got := EventType(tt.event); got != tt.want {
			t.Errorf("EventType(%T) = %q, want %q", tt.event, got, tt.want)
		}
	}
}

func TestSubscribeAllReceivesEveryType(t *testing.T) {
	bus := NewBus()
	var got []string
	bus.SubscribeAll(func(e Event) { got = append(got, EventType(e)) })

	bus.Emit(UserInput{Text: "hi"})
	bus.Emit(ThinkingStarted{})
	bus.Emit(ResponseReady{Response: "hello"})

	want := []string{"user_input", "thinking_started", "response_ready"}
	if len(got) != len(want) {
		t.Fatalf("received %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSubscribeAllRemoveDetaches(t *testing.T) {
	bus := NewBus()
	count := 0
	remove := bus.SubscribeAll(func(Event) { count++ })

	bus.Emit(Info{Message: "one"})
	remove()
	bus.Emit(Info{Message: "two"})
	remove() // idempotent

	if count != 1 {
		t.Fatalf("handler ran %d times after removal, want 1", count)
	}
}

func TestSubscribeAllDoesNotDisturbTypedSubscribers(t *testing.T) {
	bus := NewBus()
	var order []string
	Subscribe(bus, func(Info) { order = append(order, "typed") })
	bus.SubscribeAll(func(Event) { order = append(order, "all") })

	bus.Emit(Info{Message: "x"})

	if len(order) != 2 || order[0] != "typed" || order[1] != "all" {
		t.Fatalf("dispatch order = %v, want [typed all]", order)
	}
}

func TestSubscribeAllConcurrentAttachDetach(t *testing.T) {
	bus := NewBus()
	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				remove := bus.SubscribeAll(func(Event) {})
				bus.Emit(Info{Message: fmt.Sprintf("g%d-%d", i, j)})
				remove()
			}
		})
	}
	wg.Wait()

	bus.mu.RLock()
	leaked := len(bus.allHandlers)
	bus.mu.RUnlock()
	if leaked != 0 {
		t.Fatalf("%d handlers leaked after all removals", leaked)
	}
}
