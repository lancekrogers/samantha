package audio

import "sync"

// RingBuffer is a thread-safe circular buffer for audio samples.
type RingBuffer struct {
	mu   sync.Mutex
	data []float32
	size int
	head int // write position
	tail int // read position
	full bool
}

// NewRingBuffer creates a ring buffer with the given capacity in samples.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		data: make([]float32, size),
		size: size,
	}
}

// Write adds samples to the buffer. Overwrites oldest data if full.
func (rb *RingBuffer) Write(samples []float32) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for _, s := range samples {
		rb.data[rb.head] = s
		rb.head = (rb.head + 1) % rb.size

		if rb.full {
			rb.tail = (rb.tail + 1) % rb.size
		}
		if rb.head == rb.tail {
			rb.full = true
		}
	}
}

// Read returns up to n samples from the buffer.
// Returns nil if fewer than n samples are available.
func (rb *RingBuffer) Read(n int) []float32 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	avail := rb.available()
	if avail < n {
		return nil
	}

	out := make([]float32, n)
	for i := range n {
		out[i] = rb.data[rb.tail]
		rb.tail = (rb.tail + 1) % rb.size
	}
	rb.full = false
	return out
}

// Available returns the number of samples ready to read.
func (rb *RingBuffer) Available() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.available()
}

func (rb *RingBuffer) available() int {
	if rb.full {
		return rb.size
	}
	if rb.head >= rb.tail {
		return rb.head - rb.tail
	}
	return rb.size - rb.tail + rb.head
}

// Clear resets the buffer.
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.head = 0
	rb.tail = 0
	rb.full = false
}
