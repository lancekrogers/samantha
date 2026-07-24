package speaker

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
)

// CaptureSource is the mic fan-out used by live speaker feeding.
type CaptureSource interface {
	Subscribe(buffer int) (int, <-chan []float32)
	Unsubscribe(id int)
}

// LiveFeed windows mic PCM and submits speech-like spans to a LiveAdapter.
// It never blocks the capture path: full queues drop frames.
type LiveFeed struct {
	adapter *LiveAdapter
	capture CaptureSource
	window  int // samples
	hop     int
	minRMS  float64
	subID   int
	ch      <-chan []float32
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	seq     atomic.Uint64
	started time.Time
}

// StartLiveFeed begins async feeding. windowMS comes from speaker.live.window_ms.
// Returns a stop function (idempotent).
func StartLiveFeed(parent context.Context, capture CaptureSource, adapter *LiveAdapter, windowMS int) (stop func(), err error) {
	if capture == nil || adapter == nil {
		return func() {}, nil
	}
	if windowMS <= 0 {
		windowMS = 1500
	}
	window := windowMS * audio.SampleRate / 1000
	if window < minLiveEmbedSamples {
		window = minLiveEmbedSamples
	}
	hop := window / 2
	if hop < audio.ChunkSize {
		hop = audio.ChunkSize
	}

	ctx, cancel := context.WithCancel(parent)
	subID, ch := capture.Subscribe(8)
	f := &LiveFeed{
		adapter: adapter,
		capture: capture,
		window:  window,
		hop:     hop,
		minRMS:  0.01, // ignore near-silence
		subID:   subID,
		ch:      ch,
		cancel:  cancel,
		started: time.Now(),
	}
	f.wg.Add(1)
	go f.run(ctx)

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			capture.Unsubscribe(subID)
			f.wg.Wait()
		})
	}, nil
}

func (f *LiveFeed) run(ctx context.Context) {
	defer f.wg.Done()
	buf := make([]float32, 0, f.window*2)
	for {
		select {
		case <-ctx.Done():
			return
		case samples, ok := <-f.ch:
			if !ok {
				return
			}
			if len(samples) == 0 {
				continue
			}
			buf = append(buf, samples...)
			for len(buf) >= f.window {
				window := buf[:f.window]
				if rms(window) >= f.minRMS {
					id := f.seq.Add(1)
					// Session-relative timing from feed start (indicator only).
					end := time.Since(f.started)
					start := end - audio.SamplesDuration(len(window))
					if start < 0 {
						start = 0
					}
					_ = f.adapter.Submit(ctx, Segment{
						ID:      fmtSegmentID(id),
						Start:   start,
						End:     end,
						Samples: window,
						Source:  SourceLocalMic,
					})
				}
				// Hop forward; keep overlap so short words aren't clipped out.
				if f.hop >= len(buf) {
					buf = buf[:0]
					break
				}
				buf = append([]float32(nil), buf[f.hop:]...)
			}
		}
	}
}

func fmtSegmentID(n uint64) string {
	return "live-" + itoa(n)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func rms(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}
