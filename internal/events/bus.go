package events

import (
	"reflect"
	"sync"
)

// Handler is a function that handles an event.
type Handler func(Event)

// Bus is a simple typed pub/sub event bus safe for concurrent use.
type Bus struct {
	mu       sync.RWMutex
	handlers map[reflect.Type][]Handler
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[reflect.Type][]Handler),
	}
}

// Subscribe registers a handler for events of a specific type.
// The handler will be called for any event matching the registered type.
func Subscribe[T Event](b *Bus, handler func(T)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	t := reflect.TypeFor[T]()
	b.handlers[t] = append(b.handlers[t], func(e Event) {
		handler(e.(T))
	})
}

// Emit sends an event to all registered handlers for that event type.
// Handlers are called synchronously in registration order.
func (b *Bus) Emit(event Event) {
	b.mu.RLock()
	t := reflect.TypeOf(event)
	handlers := make([]Handler, len(b.handlers[t]))
	copy(handlers, b.handlers[t])
	b.mu.RUnlock()

	for _, h := range handlers {
		h(event)
	}
}
