package speaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrLiveClosed   = errors.New("speaker: live adapter closed")
	ErrLiveDropped  = errors.New("speaker: live analysis frame dropped")
	ErrLiveDisabled = errors.New("speaker: live analysis disabled")
)

type LiveStatus string

const (
	LiveDisabled    LiveStatus = "disabled"
	LiveUnavailable LiveStatus = "unavailable"
	LiveRunning     LiveStatus = "running"
	LiveDegraded    LiveStatus = "degraded"
	LiveHealthy     LiveStatus = "healthy"
	LiveClosed      LiveStatus = "closed"
)

// LiveAnalyzer is the minimum core contract needed by the asynchronous
// adapter. The concrete Analyzer and deterministic fakes both fit it.
type LiveAnalyzer interface {
	IdentifySegment(context.Context, Segment) (Observation, error)
	Reset() error
}

// LiveStats is a point-in-time, copy-safe view for TUI/CLI status.
type LiveStats struct {
	Status                LiveStatus
	QueueDepth            int
	Capacity              int
	Dropped               uint64
	Processed             uint64
	AnalyzerNanos         uint64
	LastAnalyzerNanos     uint64
	ResponsePathNanos     uint64
	LastResponsePathNanos uint64
	LastError             string
}

type liveFrame struct{ segment Segment }

// LiveAdapter fans out copied speech windows to a single analysis worker. It
// never waits for the worker while accepting a frame.
type LiveAdapter struct {
	ctx      context.Context
	cancel   context.CancelFunc
	analyzer LiveAnalyzer
	frames   chan liveFrame
	events   chan Event

	mu       sync.Mutex
	closed   bool
	enabled  bool
	status   LiveStatus
	lastErr  string
	session  uint64
	sequence uint64
	previous string
	wg       sync.WaitGroup

	dropped               atomic.Uint64
	processed             atomic.Uint64
	analyzerNanos         atomic.Uint64
	lastAnalyzerNanos     atomic.Uint64
	responsePathNanos     atomic.Uint64
	lastResponsePathNanos atomic.Uint64
}

// NewLiveAdapter creates an enabled adapter when analyzer is available. A nil
// analyzer creates a safe unavailable adapter; callers can still render status
// and continue normal conversation without special casing it.
func NewLiveAdapter(ctx context.Context, analyzer LiveAnalyzer, capacity int) *LiveAdapter {
	if ctx == nil {
		ctx = context.Background()
	}
	if capacity <= 0 {
		capacity = 4
	}
	workerCtx, cancel := context.WithCancel(ctx)
	status := LiveRunning
	if analyzer == nil {
		status = LiveUnavailable
	}
	a := &LiveAdapter{
		ctx: workerCtx, cancel: cancel, analyzer: analyzer,
		frames: make(chan liveFrame, capacity), events: make(chan Event, capacity*2),
		enabled: analyzer != nil, status: status, session: 1,
	}
	if analyzer != nil {
		a.wg.Add(1)
		go a.run()
	}
	return a
}

func (a *LiveAdapter) Events() <-chan Event { return a.events }

// Submit copies samples and returns immediately. It checks the cheap disabled
// and queue-full paths before copying PCM so unavailable/overloaded adapters
// do not allocate a frame that will never be analyzed. ErrLiveDropped is
// informational: callers may ignore it to keep the response path clean.
func (a *LiveAdapter) Submit(ctx context.Context, segment Segment) error {
	started := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		nanos := uint64(time.Since(started).Nanoseconds())
		a.responsePathNanos.Add(nanos)
		a.lastResponsePathNanos.Store(nanos)
	}()
	if a.closed {
		return ErrLiveClosed
	}
	if !a.enabled {
		return ErrLiveDisabled
	}
	if len(a.frames) >= cap(a.frames) {
		a.dropped.Add(1)
		a.status = LiveDegraded
		return ErrLiveDropped
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ctx.Done():
		return ErrLiveClosed
	default:
	}
	segment.Samples = append([]float32(nil), segment.Samples...)
	a.frames <- liveFrame{segment: segment}
	return nil
}

func (a *LiveAdapter) run() {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			a.finish()
			return
		case frame := <-a.frames:
			a.process(frame.segment)
		}
	}
}

func (a *LiveAdapter) process(segment Segment) {
	a.mu.Lock()
	if a.closed || !a.enabled {
		a.mu.Unlock()
		return
	}
	analyzer := a.analyzer
	session := a.session
	a.mu.Unlock()
	started := time.Now()
	obs, err := analyzer.IdentifySegment(a.ctx, segment)
	nanos := uint64(time.Since(started).Nanoseconds())
	a.analyzerNanos.Add(nanos)
	a.lastAnalyzerNanos.Store(nanos)
	a.processed.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || session != a.session {
		return
	}
	a.sequence++
	ev := Event{Kind: EventSpeakerUpdated, Observation: obs, SessionID: fmt.Sprintf("session-%d", session), Sequence: a.sequence}
	if err != nil {
		ev.Observation.SegmentID = segment.ID
		ev.Observation.StartMS = MS(segment.Start)
		ev.Observation.EndMS = MS(segment.End)
		ev.Observation.Source = segment.Source
		a.status = LiveDegraded
		a.lastErr = err.Error()
		ev.Observation.Label = LabelUnknown
		ev.Observation.State = StateRejected
	} else {
		a.status = LiveHealthy
		a.lastErr = ""
		if a.previous != "" && !LabelsEqual(a.previous, obs.Label) {
			ev.Kind = EventSpeakerChanged
		}
		a.previous = obs.Label
	}
	select {
	case a.events <- ev:
	default:
		// Event consumers are optional and lossy by design.
	}
}

// SetEnabled changes analysis without touching the caller's audio path.
func (a *LiveAdapter) SetEnabled(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	if a.analyzer == nil {
		a.enabled = false
		a.status = LiveUnavailable
		return
	}
	a.enabled = enabled
	if !a.enabled {
		a.status = LiveDisabled
		return
	}
	if a.status == LiveDisabled || a.status == LiveUnavailable {
		a.status = LiveRunning
	}
}

// Reset starts a new session and clears the prior speaker-change baseline.
func (a *LiveAdapter) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrLiveClosed
	}
	if a.analyzer != nil {
		if err := a.analyzer.Reset(); err != nil {
			return err
		}
	}
	a.session++
	a.sequence = 0
	a.previous = ""
	return nil
}

func (a *LiveAdapter) Stats() LiveStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return LiveStats{
		Status:                a.status,
		QueueDepth:            len(a.frames),
		Capacity:              cap(a.frames),
		Dropped:               a.dropped.Load(),
		Processed:             a.processed.Load(),
		AnalyzerNanos:         a.analyzerNanos.Load(),
		LastAnalyzerNanos:     a.lastAnalyzerNanos.Load(),
		ResponsePathNanos:     a.responsePathNanos.Load(),
		LastResponsePathNanos: a.lastResponsePathNanos.Load(),
		LastError:             a.lastErr,
	}
}

func (a *LiveAdapter) finish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.status = LiveClosed
	}
}

// Close stops the worker and closes the event stream after in-flight analysis
// has returned. It is safe to call repeatedly.
func (a *LiveAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.enabled = false
	a.status = LiveClosed
	a.mu.Unlock()
	a.cancel()
	a.wg.Wait()
	close(a.events)
	return nil
}
