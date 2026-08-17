package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// Options configures a Manager. Only Root is required; everything else has a
// protocol default, and the clock is injected so the janitor is testable
// without sleeping.
type Options struct {
	// Root is the directory meeting bundles are created in.
	Root string
	// SegmentSeconds and OutboxCapSegments are advertised to clients at start.
	SegmentSeconds    int
	OutboxCapSegments int
	// StallTimeout is how long a recording may go without a segment or
	// control event before it is marked interrupted.
	StallTimeout time.Duration
	// AbandonTimeout is the further wait, after being marked interrupted,
	// before the partial recording is processed and the bundle closed.
	// Defaults to StallTimeout, so a client has one full stall window to
	// reconnect and push its tail before results are produced without it.
	AbandonTimeout time.Duration
	// SweepInterval paces RunJanitor.
	SweepInterval time.Duration
	// Retention is how long a finished meeting stays pollable.
	Retention time.Duration
	// ProcessTimeout bounds one transcription pass, so a wedged model cannot
	// hold the single meeting slot for the life of the process.
	ProcessTimeout time.Duration
	// MaxSessions bounds how many meetings one serve process remembers.
	MaxSessions int
	// Pipeline runs transcription and diarization after a meeting stops.
	// A nil Pipeline still records audio; results are reported as failed.
	Pipeline Pipeline
	// RoutePlan delivers a start-time route_plan once the meeting is ready.
	// Nil is not an error: the plan is still written to the bundle, and
	// `meeting sweep` delivers it later.
	RoutePlan RoutePlanFunc
	// STTLabel is recorded in the bundle header, matching what the desktop
	// recorder writes.
	STTLabel string
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

func (o Options) normalize() Options {
	if o.SegmentSeconds <= 0 {
		o.SegmentSeconds = DefaultSegmentSeconds
	}
	if o.OutboxCapSegments <= 0 {
		o.OutboxCapSegments = DefaultOutboxCapSegments
	}
	if o.StallTimeout <= 0 {
		o.StallTimeout = DefaultStallTimeout
	}
	if o.AbandonTimeout <= 0 {
		o.AbandonTimeout = o.StallTimeout
	}
	if o.SweepInterval <= 0 {
		o.SweepInterval = DefaultSweepInterval
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.ProcessTimeout <= 0 {
		o.ProcessTimeout = DefaultProcessTimeout
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = DefaultMaxSessions
	}
	if o.STTLabel == "" {
		o.STTLabel = "remote"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Manager owns every remote meeting on one serve process. Exactly one meeting
// may be live at a time — the constraint the protocol's 409 expresses — and it
// shares no state with the dispatcher or the WebSocket hub.
type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager prepares the meetings root and returns a ready manager.
func NewManager(opts Options) (*Manager, error) {
	opts = opts.normalize()
	if opts.Root == "" {
		return nil, fmt.Errorf("meeting: manager root is required")
	}
	// 0o700: bundle names carry meeting titles and the contents are private.
	if err := os.MkdirAll(opts.Root, 0o700); err != nil {
		return nil, fmt.Errorf("meeting: create meetings dir: %w", err)
	}
	return &Manager{opts: opts, sessions: make(map[string]*Session)}, nil
}

// SegmentSeconds and OutboxCapSegments are the client-facing capture knobs.
func (m *Manager) SegmentSeconds() int    { return m.opts.SegmentSeconds }
func (m *Manager) OutboxCapSegments() int { return m.opts.OutboxCapSegments }

// Start creates a bundle and begins a meeting. It fails with ErrMeetingActive
// while another meeting still holds an open bundle.
func (m *Manager) Start(ctx context.Context, req StartRequest) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if live := m.liveLocked(); live != nil {
		return nil, fmt.Errorf("%w (id %s)", ErrMeetingActive, live.id)
	}
	// Finished meetings age out on Retention, but nothing else bounds the map
	// or the bundles on disk; drop the oldest rather than grow forever.
	m.evictOldestLocked()

	now := m.opts.Now()
	title := sanitizeTitle(req.Title)
	source, err := normalizeSource(req.Source)
	if err != nil {
		return nil, err
	}
	// Validate the plan before anything exists on disk: a malformed request
	// must not leave an orphan bundle behind.
	plan, err := normalizeRoutePlan(req.RoutePlan)
	if err != nil {
		return nil, err
	}
	id, err := newMeetingID()
	if err != nil {
		return nil, err
	}
	bundlePath, writer, err := createBundle(m.opts.Root, title, m.opts.STTLabel, source, id, now)
	if err != nil {
		return nil, err
	}
	// Before the first segment, so a meeting that dies mid-recording still
	// leaves a plan the sweep can deliver — the crash-safe ordering the
	// desktop recorder already has.
	if plan.destID != "" {
		if err := writer.WriteRoutePlan(plan.destID, plan.body); err != nil {
			_, _ = writer.Close()
			return nil, err
		}
	}
	segments, err := newSegmentStore(bundlePath, m.opts.SegmentSeconds)
	if err != nil {
		_, _ = writer.Close()
		return nil, err
	}
	session := &Session{
		id: id, bundlePath: bundlePath, title: title, campaign: req.Campaign,
		startedAt: writer.StartedAt(), segments: segments, pipeline: m.opts.Pipeline,
		now: m.opts.Now, processTimeout: m.opts.ProcessTimeout,
		writer: writer, state: StateRecording, lastActivity: now, lastSeq: -1,
		plan: plan, deliverPlanFn: m.opts.RoutePlan,
	}
	m.sessions[id] = session
	return session, nil
}

// Session looks one meeting up by id.
func (m *Manager) Session(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return session, nil
}

// Live returns the meeting currently holding an open bundle, if any.
func (m *Manager) Live() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveLocked()
}

func (m *Manager) liveLocked() *Session {
	for _, session := range m.sessions {
		if session.live() {
			return session
		}
	}
	return nil
}

// Now exposes the manager's clock so handlers stamp activity consistently.
func (m *Manager) Now() time.Time { return m.opts.Now() }

// Sweep is the janitor's unit of work, exported so tests drive it with an
// explicit clock instead of waiting on wall time. A quiet recording is marked
// interrupted; a meeting that stays quiet after that is processed and closed;
// long-finished meetings are forgotten.
func (m *Manager) Sweep(ctx context.Context, now time.Time) {
	for _, session := range m.snapshot() {
		if err := ctx.Err(); err != nil {
			return
		}
		state, last, finished := session.sweepState()
		switch {
		case state == StateRecording && now.Sub(last) >= m.opts.StallTimeout:
			session.markInterrupted(now)
		case state == StateInterrupted && session.live() && now.Sub(last) >= m.opts.AbandonTimeout:
			session.abandon(ctx, now)
		case !session.live() && !finished.IsZero() && now.Sub(finished) >= m.opts.Retention:
			m.forget(session.id)
		}
	}
}

// RunJanitor sweeps until ctx is done. It is the only background goroutine the
// meeting surface owns and it exits with serve.
func (m *Manager) RunJanitor(ctx context.Context) {
	ticker := time.NewTicker(m.opts.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep(ctx, m.opts.Now())
		}
	}
}

// Close shuts any open bundle without running the pipeline: serve is going
// down, and a closed partial bundle beats one with no trailer at all.
func (m *Manager) Close() error {
	var firstErr error
	for _, session := range m.snapshot() {
		if err := session.closeBundle(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) snapshot() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// evictOldestLocked drops finished sessions until the registry is back under
// its cap. Only closed meetings are candidates; a live one is never evicted.
func (m *Manager) evictOldestLocked() {
	for len(m.sessions) >= m.opts.MaxSessions {
		oldestID, oldest := "", time.Time{}
		for id, session := range m.sessions {
			_, _, finished := session.sweepState()
			if finished.IsZero() {
				continue
			}
			if oldestID == "" || finished.Before(oldest) {
				oldestID, oldest = id, finished
			}
		}
		if oldestID == "" {
			return
		}
		delete(m.sessions, oldestID)
	}
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// sweepState reports what the janitor needs without exposing the session lock.
func (s *Session) sweepState() (State, time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.lastActivity, s.finishedAt
}

// maxTitleRunes keeps a network-supplied title to something a bundle header
// and a picker row can both live with.
const maxTitleRunes = 120

// sanitizeTitle makes a client-supplied title safe to write into the bundle's
// human header. Line breaks are the real hazard: the header is line-oriented,
// so an unfiltered title could forge "# Started:" lines in meeting.md.
func sanitizeTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > maxTitleRunes {
		title = strings.TrimSpace(string(runes[:maxTitleRunes]))
	}
	if title == "" {
		return "Meeting"
	}
	return title
}

// createBundle creates the meeting directory, retrying once with the meeting
// id appended. Bundle names are second-resolution, so a stop-then-start inside
// one second would otherwise collide and fail the client's request.
func createBundle(root, title, sttLabel, source, id string, now time.Time) (string, *meetinglog.Writer, error) {
	name := meeting.BundleName(title, now)
	path := filepath.Join(root, name)
	writer, err := meetinglog.CreateBundle(path, title, sttLabel, source)
	if err == nil {
		return path, writer, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", nil, err
	}
	unique := strings.TrimSuffix(name, meeting.BundleSuffix) + "-" + id + meeting.BundleSuffix
	path = filepath.Join(root, unique)
	writer, err = meetinglog.CreateBundle(path, title, sttLabel, source)
	if err != nil {
		return "", nil, err
	}
	return path, writer, nil
}

// normalizeSource validates the capture surface a start request claims.
// Empty means ios: phones were the only clients before the field existed, so
// absence keeps meaning what it always did.
func normalizeSource(source string) (string, error) {
	switch source {
	case "":
		return "ios", nil
	case "ios", "mac", "watch":
		return source, nil
	}
	return "", fmt.Errorf("%w %q", ErrBadStart, source)
}

// newMeetingID mints an opaque handle, matching the intent sink's id shape.
func newMeetingID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("meeting: generate id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
