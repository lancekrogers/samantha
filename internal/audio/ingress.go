package audio

import (
	"context"
	"sync"
	"time"
)

// Ingress is a live FrameSource fed by Write (e.g. remote mic PCM over the
// network). Call Finalize to end the current utterance with an explicit Final
// frame so offline/endpoint STT can commit without waiting on silence timeouts.
// Reset prepares for the next utterance.
//
// Thread-safe: Write/Finalize from network handlers; ReadFrame from STT.
type Ingress struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]float32
	final  bool
	closed bool
	seq    int64
}

// NewIngress builds an empty remote audio ingress.
func NewIngress() *Ingress {
	i := &Ingress{}
	i.cond = sync.NewCond(&i.mu)
	return i
}

// Write appends mono float32 samples at SampleRate. Empty slices are ignored.
func (i *Ingress) Write(samples []float32) error {
	if len(samples) == 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return ErrSourceClosed
	}
	if i.final {
		return nil // utterance already finalized; drop late frames
	}
	chunk := make([]float32, len(samples))
	copy(chunk, samples)
	i.chunks = append(i.chunks, chunk)
	i.cond.Signal()
	return nil
}

// Finalize marks the end of the current utterance. Subsequent ReadFrame calls
// drain remaining samples then return one Final frame.
func (i *Ingress) Finalize() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.final {
		return
	}
	i.final = true
	i.cond.Broadcast()
}

// Reset clears buffers for a new utterance (after a completed turn).
func (i *Ingress) Reset() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.chunks = nil
	i.final = false
	i.seq = 0
	// closed stays closed
	i.cond.Broadcast()
}

// Read implements the legacy audioSource contract for STT providers that
// still use Read().
func (i *Ingress) Read() []float32 {
	frame, err := i.ReadFrame(context.Background())
	if err != nil || frame.Final || len(frame.Samples) == 0 {
		return nil
	}
	return frame.Samples
}

// ReadFrame implements FrameSource. Blocks until samples, Final, close, or ctx cancel.
func (i *Ingress) ReadFrame(ctx context.Context) (Frame, error) {
	// Wake the cond when ctx is canceled so we do not leak blocked readers.
	stop := context.AfterFunc(ctx, func() {
		i.mu.Lock()
		i.cond.Broadcast()
		i.mu.Unlock()
	})
	if stop != nil {
		defer stop()
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	for {
		if i.closed {
			return Frame{}, ErrSourceClosed
		}
		if err := ctx.Err(); err != nil {
			return Frame{}, err
		}
		if len(i.chunks) > 0 {
			chunk := i.chunks[0]
			i.chunks = i.chunks[1:]
			i.seq++
			return Frame{
				Samples:    chunk,
				SampleRate: SampleRate,
				Channels:   Channels,
				Duration:   SamplesDuration(len(chunk)),
				Sequence:   i.seq,
				SourceKind: SourceLive,
				StartedAt:  time.Now(),
			}, nil
		}
		if i.final {
			// One Final frame, then wait for Reset (or further reads see Final again).
			// Offline STT wants a single Final; subsequent reads return ErrNoFrameReady
			// until Reset clears final for the next turn.
			i.final = false
			return Frame{SourceKind: SourceFixture, Final: true}, nil
		}
		i.cond.Wait()
	}
}

// Close permanently shuts down the ingress.
func (i *Ingress) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed = true
	i.cond.Broadcast()
	return nil
}

// Ensure Ingress satisfies the contracts STT expects.
var (
	_ FrameSource = (*Ingress)(nil)
)
