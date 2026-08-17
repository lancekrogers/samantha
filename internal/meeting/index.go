package meeting

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// The meeting index is the history surface: one pass over a bundle's event
// stream produces everything a list row needs, so `GET /v1/meetings`,
// `meeting list`, and the Mac's Meetings screen all read the same derivation
// instead of each inventing its own.
//
// Two rules shape the whole file. A bundle that cannot be read is skipped, not
// failed — one half-written directory must never blank a user's history. And
// nothing here derives what summaryFromEvents already derives; a second
// deriver would drift from the summary a route renders.

// Index bounds. A client may ask for fewer or more, never more than the max:
// the response is built in memory and a runaway limit is a memory bug.
const (
	DefaultIndexLimit = 200
	MaxIndexLimit     = 1000
)

// Bundle states as seen from disk. A live session overlays its own state
// (recording, processing) on top of these; see internal/netapi.
const (
	// BundleStateReady means the bundle carries a session_end trailer.
	BundleStateReady = "ready"
	// BundleStateInterrupted means it does not: the recorder died, the agent
	// restarted, or the janitor never got to finalize it.
	BundleStateInterrupted = "interrupted"
)

// Route statuses reported per bundle.
const (
	RouteStatusNone    = "none"
	RouteStatusPlanned = "planned"
	RouteStatusRouted  = "routed"
	RouteStatusFailed  = "failed"
)

// BundleEntry is one meeting as seen from disk.
type BundleEntry struct {
	ID                  string       `json:"id"`     // bundle dir name
	Bundle              string       `json:"bundle"` // absolute path
	Document            string       `json:"document"`
	EventsFile          string       `json:"events_file"`
	Description         string       `json:"description"`
	Source              string       `json:"source,omitempty"` // mac | ios | watch | ""
	State               string       `json:"state"`
	StartedAt           time.Time    `json:"started_at"`
	EndedAt             time.Time    `json:"ended_at,omitempty"`
	DurationSeconds     int64        `json:"duration_seconds"`
	Utterances          int          `json:"utterances"`
	Notes               int          `json:"notes"`
	Bookmarks           int          `json:"bookmarks"`
	Errors              int          `json:"errors"`
	SpeakerStatus       string       `json:"speaker_status,omitempty"`
	SpeakerCount        int          `json:"speaker_count,omitempty"`
	SpeakerAnalysisFile string       `json:"speaker_analysis_file,omitempty"`
	AudioFile           string       `json:"audio_file,omitempty"`
	Route               *RouteStatus `json:"route,omitempty"`
}

// RouteStatus is the filing history of one bundle, derived from its durable
// route_plan / routed / route_failed events.
type RouteStatus struct {
	Status        string    `json:"status"`
	DestinationID string    `json:"destination_id,omitempty"`
	Type          string    `json:"type,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	At            time.Time `json:"at,omitempty"`
	// Attempts counts durable route_failed events.
	Attempts int `json:"attempts"`
	// Retryable reports whether SweepPendingRoutes would pick this bundle up
	// right now — the same predicate, so the UI's "Retry pending routes"
	// promises exactly what the sweep delivers.
	Retryable bool `json:"retryable"`
}

// Summary projects the entry back onto the shape every routing and rendering
// path already speaks, so a finished bundle can be routed or reported without
// re-reading its event stream.
func (e BundleEntry) Summary() meetinglog.Summary {
	return meetinglog.Summary{
		Description:         e.Description,
		Bundle:              e.Bundle,
		File:                e.Document,
		JSONLFile:           e.EventsFile,
		StartedAt:           e.StartedAt,
		EndedAt:             e.EndedAt,
		DurationSeconds:     e.DurationSeconds,
		Utterances:          e.Utterances,
		Notes:               e.Notes,
		Bookmarks:           e.Bookmarks,
		Errors:              e.Errors,
		SpeakerStatus:       e.SpeakerStatus,
		SpeakerCount:        e.SpeakerCount,
		SpeakerAnalysisFile: e.SpeakerAnalysisFile,
		AudioFile:           e.AudioFile,
	}
}

// IndexOptions bounds one index read.
type IndexOptions struct {
	// Limit caps the returned entries, newest first. Zero means
	// DefaultIndexLimit; anything above MaxIndexLimit is clamped.
	Limit int
	// Since drops meetings that started before it. Zero means no floor.
	Since time.Time
}

func (o IndexOptions) limit() int {
	switch {
	case o.Limit <= 0:
		return DefaultIndexLimit
	case o.Limit > MaxIndexLimit:
		return MaxIndexLimit
	default:
		return o.Limit
	}
}

// Index lists the meeting bundles under dir, newest first. A missing dir is an
// empty history, not an error; only a dir that exists and cannot be read is.
// truncated reports that Limit dropped entries.
func Index(ctx context.Context, dir string, opts IndexOptions) (entries []BundleEntry, truncated bool, err error) {
	return newCache().Index(ctx, dir, opts)
}

// Cache serves unchanged bundles from their already-parsed entry, keyed by the
// event stream's (modtime, size). After the first read, listing a meetings dir
// is a stat walk — which is what makes polling GET /v1/meetings cheap.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cachedEntry
}

type cachedEntry struct {
	modTime time.Time
	size    int64
	entry   BundleEntry
}

// NewCache returns an empty index cache.
func NewCache() *Cache { return newCache() }

func newCache() *Cache { return &Cache{entries: make(map[string]cachedEntry)} }

// Index lists dir through the cache.
func (c *Cache) Index(ctx context.Context, dir string, opts IndexOptions) ([]BundleEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	bundles, err := listBundles(dir)
	if err != nil {
		return nil, false, err
	}
	entries := make([]BundleEntry, 0, len(bundles))
	seen := make(map[string]struct{}, len(bundles))
	for _, bundle := range bundles {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		entry, ok := c.entryFor(bundle)
		if !ok {
			continue
		}
		seen[bundle] = struct{}{}
		if !opts.Since.IsZero() && entry.StartedAt.Before(opts.Since) {
			continue
		}
		entries = append(entries, entry)
	}
	c.prune(seen)
	sortEntries(entries)
	if limit := opts.limit(); len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

// Warm pre-parses the newest bundles so ids resolve the moment serve is up.
// "Newest" here is the event stream's mtime — a cheap stat rather than a read
// — because this is a cache prefill, not the ordering the index reports.
// Everything it does not reach is parsed on the first request that needs it.
func (c *Cache) Warm(ctx context.Context, dir string, limit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bundles, err := listBundles(dir)
	if err != nil {
		return err
	}
	type stamped struct {
		bundle  string
		modTime time.Time
	}
	stamps := make([]stamped, 0, len(bundles))
	for _, bundle := range bundles {
		info, err := os.Stat(bundleEventsPath(bundle))
		if err != nil {
			continue
		}
		stamps = append(stamps, stamped{bundle: bundle, modTime: info.ModTime()})
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].modTime.After(stamps[j].modTime) })
	if limit <= 0 {
		limit = DefaultIndexLimit
	}
	for i, stamp := range stamps {
		if i >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c.entryFor(stamp.bundle)
	}
	return nil
}

// Entry reports one bundle by absolute path, reading it through the cache.
// Unreadable bundles report false rather than an error: the caller's answer is
// "no such meeting" either way.
func (c *Cache) Entry(ctx context.Context, bundle string) (BundleEntry, bool) {
	if ctx.Err() != nil {
		return BundleEntry{}, false
	}
	return c.entryFor(bundle)
}

// entryFor returns the cached entry when the event stream has not changed
// since it was parsed, and re-reads it when it has.
func (c *Cache) entryFor(bundle string) (BundleEntry, bool) {
	info, err := os.Stat(bundleEventsPath(bundle))
	if err != nil || info.IsDir() {
		return BundleEntry{}, false
	}
	c.mu.Lock()
	cached, ok := c.entries[bundle]
	c.mu.Unlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.entry, true
	}
	entry, ok := readBundleEntry(bundle)
	if !ok {
		return BundleEntry{}, false
	}
	c.mu.Lock()
	c.entries[bundle] = cachedEntry{modTime: info.ModTime(), size: info.Size(), entry: entry}
	c.mu.Unlock()
	return entry, true
}

// prune forgets bundles that are no longer on disk, so a long-lived serve does
// not hold entries for meetings the user deleted.
func (c *Cache) prune(seen map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for bundle := range c.entries {
		if _, ok := seen[bundle]; !ok {
			delete(c.entries, bundle)
		}
	}
}

// listBundles returns the absolute path of every *.meeting directory in dir,
// using the same predicate as SweepPendingRoutes.
func listBundles(dir string) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	listing, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A meetings dir that was never created is an empty history.
			return nil, nil
		}
		return nil, fmt.Errorf("meeting: list meetings dir: %w", err)
	}
	bundles := make([]string, 0, len(listing))
	for _, entry := range listing {
		if !entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), BundleSuffix) {
			continue
		}
		bundles = append(bundles, filepath.Join(dir, entry.Name()))
	}
	return bundles, nil
}

// sortEntries orders newest first, breaking ties on the bundle id so two
// same-second recordings never swap places between reads.
func sortEntries(entries []BundleEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].StartedAt.Equal(entries[j].StartedAt) {
			return entries[i].ID > entries[j].ID
		}
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
}

// readBundleEntry parses one bundle. It reads the event stream exactly once
// and hands it to summaryFromEvents — the same derivation a route uses — then
// adds the fields only a directory listing can answer.
func readBundleEntry(bundle string) (BundleEntry, bool) {
	jsonlPath := bundleEventsPath(bundle)
	events, err := ReadEvents(jsonlPath)
	if err != nil {
		return BundleEntry{}, false
	}
	summary := summaryFromEvents(jsonlPath, bundle, events)
	entry := BundleEntry{
		ID:                  filepath.Base(bundle),
		Bundle:              bundle,
		Document:            filepath.Join(bundle, meetinglog.BundleDocumentName),
		EventsFile:          jsonlPath,
		Description:         summary.Description,
		State:               BundleStateInterrupted,
		StartedAt:           summary.StartedAt,
		EndedAt:             summary.EndedAt,
		DurationSeconds:     summary.DurationSeconds,
		Utterances:          summary.Utterances,
		Notes:               summary.Notes,
		Bookmarks:           summary.Bookmarks,
		Errors:              summary.Errors,
		SpeakerStatus:       summary.SpeakerStatus,
		SpeakerCount:        summary.SpeakerCount,
		SpeakerAnalysisFile: summary.SpeakerAnalysisFile,
		AudioFile:           retainedAudioPath(bundle),
	}
	for _, event := range events {
		switch event.Type {
		case meetinglog.TypeSessionStart:
			entry.Source = event.Source
		case meetinglog.TypeSessionEnd:
			entry.State = BundleStateReady
		}
	}
	entry.Route = routeStatus(events, entry.State == BundleStateReady)
	return entry, true
}

// retainedAudioPath reports the bundle's audio only when it was kept.
// speaker.meeting.record_audio=false streams to a transient audio.wav.part
// that is deleted after diarization — reporting it would promise a
// re-analysis that cannot happen.
func retainedAudioPath(bundle string) string {
	path := filepath.Join(bundle, meetinglog.BundleAudioName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return path
}

// routeStatus derives the filing history with the sweep's rules, so the UI and
// the retry loop always agree on what is pending.
func routeStatus(events []meetinglog.Event, ended bool) *RouteStatus {
	var (
		status  RouteStatus
		planned bool
		endedAt time.Time
	)
	for _, event := range events {
		switch event.Type {
		case meetinglog.TypeRoutePlan:
			planned = true
			status.DestinationID = event.Label
		case meetinglog.TypeSessionEnd:
			endedAt = parseEventTime(event.TS)
		case TypeRouted:
			status.Status = RouteStatusRouted
			applyRouteEvent(&status, event)
		case TypeRouteFailed:
			status.Attempts++
			if status.Status != RouteStatusRouted {
				applyRouteEvent(&status, event)
			}
		}
	}
	switch {
	case status.Status == RouteStatusRouted:
	case planned && status.Attempts > 0:
		status.Status = RouteStatusFailed
	case planned:
		status.Status = RouteStatusPlanned
	default:
		return &RouteStatus{Status: RouteStatusNone}
	}
	status.Retryable = planned && ended && status.Status != RouteStatusRouted &&
		status.Attempts < SweepMaxAttempts &&
		(endedAt.IsZero() || time.Since(endedAt) <= SweepWindow)
	return &status
}

// applyRouteEvent copies one provenance event onto the status. The event's
// Message is "<outcome>: <detail>" (provenance.go), so the detail is what
// follows the first separator.
func applyRouteEvent(status *RouteStatus, event meetinglog.Event) {
	if event.Label != "" {
		status.DestinationID = event.Label
	}
	status.Type = event.Text
	if _, detail, ok := strings.Cut(event.Message, ": "); ok {
		status.Detail = detail
	} else {
		status.Detail = event.Message
	}
	status.At = parseEventTime(event.TS)
}

func parseEventTime(ts string) time.Time {
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return at
}
