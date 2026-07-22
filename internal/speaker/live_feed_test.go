package speaker

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeCapture struct {
	mu   sync.Mutex
	subs map[int]chan []float32
	next int
}

func newFakeCapture() *fakeCapture {
	return &fakeCapture{subs: make(map[int]chan []float32)}
}

func (c *fakeCapture) Subscribe(buffer int) (int, <-chan []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if buffer <= 0 {
		buffer = 1
	}
	c.next++
	ch := make(chan []float32, buffer)
	c.subs[c.next] = ch
	return c.next, ch
}

func (c *fakeCapture) Unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.subs[id]; ok {
		close(ch)
		delete(c.subs, id)
	}
}

func (c *fakeCapture) publish(samples []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- append([]float32(nil), samples...):
		default:
		}
	}
}

func TestStartLiveFeedSubmitsSpeechWindows(t *testing.T) {
	fake := &liveFake{label: "speaker-1"}
	adapter := NewLiveAdapter(context.Background(), fake, 8)
	defer adapter.Close()

	cap := newFakeCapture()
	// 500ms window = minLiveEmbedSamples (8000) at 16 kHz.
	stop, err := StartLiveFeed(context.Background(), cap, adapter, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Loud samples so RMS gate passes.
	chunk := make([]float32, 1600)
	for i := range chunk {
		chunk[i] = 0.2
	}
	// 500ms window = 8000 samples → 5 chunks of 1600; send 6 for a hop.
	for i := 0; i < 6; i++ {
		cap.publish(chunk)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for live feed to process")
		default:
			if adapter.Stats().Processed > 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestRMS(t *testing.T) {
	if rms(nil) != 0 {
		t.Fatal("empty rms")
	}
	if got := rms([]float32{0, 0, 0}); got != 0 {
		t.Fatalf("silent rms = %v", got)
	}
	if got := rms([]float32{1, -1}); got < 0.9 {
		t.Fatalf("rms = %v", got)
	}
}
