package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleSubscriber(t *testing.T) {
	bus := NewBus()
	var received string

	Subscribe(bus, func(e Info) {
		received = e.Message
	})

	bus.Emit(Info{Message: "hello"})

	if received != "hello" {
		t.Errorf("received = %q, want hello", received)
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewBus()
	var count int

	Subscribe(bus, func(e Info) { count++ })
	Subscribe(bus, func(e Info) { count++ })
	Subscribe(bus, func(e Info) { count++ })

	bus.Emit(Info{Message: "test"})

	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestTypedRouting(t *testing.T) {
	bus := NewBus()
	var infoCount, errorCount int

	Subscribe(bus, func(e Info) { infoCount++ })
	Subscribe(bus, func(e Error) { errorCount++ })

	bus.Emit(Info{Message: "info"})
	bus.Emit(Info{Message: "info2"})
	bus.Emit(Error{Message: "error"})

	if infoCount != 2 {
		t.Errorf("infoCount = %d, want 2", infoCount)
	}
	if errorCount != 1 {
		t.Errorf("errorCount = %d, want 1", errorCount)
	}
}

func TestConcurrentEmit(t *testing.T) {
	bus := NewBus()
	var count atomic.Int64

	Subscribe(bus, func(e Info) {
		count.Add(1)
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit(Info{Message: "concurrent"})
		}()
	}
	wg.Wait()

	if count.Load() != 100 {
		t.Errorf("count = %d, want 100", count.Load())
	}
}

func TestEventFields(t *testing.T) {
	bus := NewBus()
	var received ThinkingComplete

	Subscribe(bus, func(e ThinkingComplete) {
		received = e
	})

	bus.Emit(ThinkingComplete{
		Response:     "short",
		FullResponse: "full long response",
		Elapsed:      2 * time.Second,
	})

	if received.Response != "short" {
		t.Errorf("Response = %q, want short", received.Response)
	}
	if received.Elapsed != 2*time.Second {
		t.Errorf("Elapsed = %v, want 2s", received.Elapsed)
	}
}

func TestNoSubscribers(t *testing.T) {
	bus := NewBus()
	// Should not panic
	bus.Emit(Info{Message: "no one listening"})
}
