package meeting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// writeBundle fabricates a bundle from an exact event stream. The index reads
// nothing but events.jsonl and the audio file, so this is the whole input.
func writeBundle(t *testing.T, dir, name string, events []meetinglog.Event) string {
	t.Helper()
	bundle := filepath.Join(dir, name)
	internal := filepath.Join(bundle, meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(bundleEventsPath(bundle), []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func started(at time.Time, description, source string) meetinglog.Event {
	return meetinglog.Event{
		Type: meetinglog.TypeSessionStart, TS: at.Format(time.RFC3339),
		Desc: description, Source: source,
	}
}

func ended(at time.Time) meetinglog.Event {
	return meetinglog.Event{Type: meetinglog.TypeSessionEnd, TS: at.Format(time.RFC3339)}
}

func indexAll(t *testing.T, dir string) []BundleEntry {
	t.Helper()
	entries, truncated, err := Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if truncated {
		t.Fatal("Index() truncated an unlimited read")
	}
	return entries
}

func TestIndexMissingDirIsEmptyNotAnError(t *testing.T) {
	entries, truncated, err := Index(context.Background(), filepath.Join(t.TempDir(), "never-created"), IndexOptions{})
	if err != nil {
		t.Fatalf("Index() on a missing dir error = %v, want nil", err)
	}
	if len(entries) != 0 || truncated {
		t.Fatalf("Index() = %d entries (truncated %v), want an empty history", len(entries), truncated)
	}
}

func TestIndexCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Index(ctx, t.TempDir(), IndexOptions{}); err == nil {
		t.Fatal("Index() with a cancelled context returned no error")
	}
}

func TestIndexSkipsUnreadableBundles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	good := writeBundle(t, dir, "good-20260816-101500.meeting",
		[]meetinglog.Event{started(now, "Weekly sync", "mac"), ended(now.Add(time.Minute))})

	// A bundle whose creation died before the event stream existed.
	if err := os.MkdirAll(filepath.Join(dir, "half-20260816-101600.meeting", meetinglog.BundleInternalDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	// A bundle whose event stream is not JSON.
	torn := filepath.Join(dir, "torn-20260816-101700.meeting")
	if err := os.MkdirAll(filepath.Join(torn, meetinglog.BundleInternalDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundleEventsPath(torn), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A plain directory that is not a bundle at all.
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries := indexAll(t, dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want the one readable bundle: %+v", len(entries), entries)
	}
	if entries[0].Bundle != good {
		t.Fatalf("bundle = %q, want %q", entries[0].Bundle, good)
	}
}

func TestIndexDerivesEntryFromTheEventStream(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	stop := start.Add(32*time.Minute + 12*time.Second)
	bundle := writeBundle(t, dir, "weekly-sync-20260816-101500.meeting", []meetinglog.Event{
		started(start, "Weekly sync", "mac"),
		{Type: meetinglog.TypeUtterance, TS: start.Format(time.RFC3339), Text: "hello"},
		{Type: meetinglog.TypeNote, TS: start.Format(time.RFC3339), Text: "ship it"},
		{Type: meetinglog.TypeBookmark, TS: start.Format(time.RFC3339), Label: "important"},
		{Type: meetinglog.TypeError, TS: start.Format(time.RFC3339), Message: "stt hiccup"},
		{Type: meetinglog.TypeSpeakerAnalysis, TS: stop.Format(time.RFC3339), Status: "complete",
			SpeakerCount: 3, Artifact: "/tmp/speaker-analysis.json"},
		ended(stop),
	})

	entries := indexAll(t, dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	got := entries[0]
	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"id", got.ID, "weekly-sync-20260816-101500.meeting"},
		{"bundle", got.Bundle, bundle},
		{"document", got.Document, filepath.Join(bundle, meetinglog.BundleDocumentName)},
		{"events_file", got.EventsFile, bundleEventsPath(bundle)},
		{"description", got.Description, "Weekly sync"},
		{"source", got.Source, "mac"},
		{"state", got.State, BundleStateReady},
		{"duration_seconds", got.DurationSeconds, int64(1932)},
		{"utterances", got.Utterances, 1},
		{"notes", got.Notes, 1},
		{"bookmarks", got.Bookmarks, 1},
		{"errors", got.Errors, 1},
		{"speaker_status", got.SpeakerStatus, "complete"},
		{"speaker_count", got.SpeakerCount, 3},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if !got.StartedAt.Equal(start) || !got.EndedAt.Equal(stop) {
		t.Errorf("times = %s..%s, want %s..%s", got.StartedAt, got.EndedAt, start, stop)
	}
}

func TestIndexStateFollowsSessionEnd(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	writeBundle(t, dir, "done-20260816-101500.meeting",
		[]meetinglog.Event{started(now, "Finished", ""), ended(now.Add(time.Minute))})
	writeBundle(t, dir, "open-20260816-101600.meeting",
		[]meetinglog.Event{started(now.Add(time.Minute), "Cut short", "")})

	byID := make(map[string]BundleEntry)
	for _, entry := range indexAll(t, dir) {
		byID[entry.ID] = entry
	}
	if got := byID["done-20260816-101500.meeting"].State; got != BundleStateReady {
		t.Errorf("closed bundle state = %q, want ready", got)
	}
	if got := byID["open-20260816-101600.meeting"].State; got != BundleStateInterrupted {
		t.Errorf("trailerless bundle state = %q, want interrupted", got)
	}
}

func TestIndexOrdersNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	for i, name := range []string{"a-20260814-090000.meeting", "b-20260815-090000.meeting", "c-20260816-090000.meeting"} {
		at := base.Add(time.Duration(i) * time.Hour)
		writeBundle(t, dir, name, []meetinglog.Event{started(at, name, ""), ended(at.Add(time.Minute))})
	}
	entries := indexAll(t, dir)
	want := []string{"c-20260816-090000.meeting", "b-20260815-090000.meeting", "a-20260814-090000.meeting"}
	for i, entry := range entries {
		if entry.ID != want[i] {
			t.Fatalf("entry %d = %q, want %q (newest first)", i, entry.ID, want[i])
		}
	}
}

func TestIndexLimitAndSince(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	for i := range 5 {
		at := base.Add(time.Duration(i) * time.Hour)
		writeBundle(t, dir, "m"+string(rune('a'+i))+"-2026081"+string(rune('0'+i))+"-090000.meeting",
			[]meetinglog.Event{started(at, "meeting", ""), ended(at.Add(time.Minute))})
	}

	entries, truncated, err := Index(context.Background(), dir, IndexOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !truncated {
		t.Fatalf("limited read = %d entries (truncated %v), want 2 and truncated", len(entries), truncated)
	}

	entries, truncated, err = Index(context.Background(), dir, IndexOptions{Since: base.Add(150 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || truncated {
		t.Fatalf("since read = %d entries (truncated %v), want the 2 newest", len(entries), truncated)
	}
	for _, entry := range entries {
		if entry.StartedAt.Before(base.Add(150 * time.Minute)) {
			t.Fatalf("entry %s started before the floor", entry.ID)
		}
	}
}

func TestIndexReportsOnlyRetainedAudio(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	kept := writeBundle(t, dir, "kept-20260816-101500.meeting",
		[]meetinglog.Event{started(now, "Kept", ""), ended(now.Add(time.Minute))})
	if err := os.WriteFile(filepath.Join(kept, meetinglog.BundleAudioName), []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	// record_audio=false leaves only the transient working file, which is
	// deleted after diarization and can never be re-analyzed.
	transient := writeBundle(t, dir, "transient-20260816-101600.meeting",
		[]meetinglog.Event{started(now.Add(time.Minute), "Transient", ""), ended(now.Add(2 * time.Minute))})
	part := filepath.Join(transient, meetinglog.BundleInternalDirName, meetinglog.BundleAudioName+".part")
	if err := os.WriteFile(part, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}

	byID := make(map[string]BundleEntry)
	for _, entry := range indexAll(t, dir) {
		byID[entry.ID] = entry
	}
	if got := byID["kept-20260816-101500.meeting"].AudioFile; got != filepath.Join(kept, meetinglog.BundleAudioName) {
		t.Errorf("retained audio_file = %q, want the bundle's audio.wav", got)
	}
	if got := byID["transient-20260816-101600.meeting"].AudioFile; got != "" {
		t.Errorf("audio_file = %q, want empty — audio.wav.part is not re-analyzable", got)
	}
}

func TestIndexRouteStatusDerivation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	plan := meetinglog.Event{Type: meetinglog.TypeRoutePlan, TS: now.Format(time.RFC3339), Label: "camp:blockhead"}
	routed := meetinglog.Event{Type: TypeRouted, TS: now.Format(time.RFC3339),
		Label: "camp:blockhead", Text: TypeCampaign, Message: "routed: notes/meetings/x.md"}
	failed := meetinglog.Event{Type: TypeRouteFailed, TS: now.Format(time.RFC3339),
		Label: "camp:blockhead", Text: TypeCampaign, Message: "failed: camp: no such campaign"}

	tests := []struct {
		name          string
		events        []meetinglog.Event
		want          RouteStatus
		wantRetryable bool
	}{
		{
			name:   "never planned, never routed",
			events: []meetinglog.Event{ended(now)},
			want:   RouteStatus{Status: RouteStatusNone},
		},
		{
			name:          "planned but not yet delivered",
			events:        []meetinglog.Event{plan, ended(now)},
			want:          RouteStatus{Status: RouteStatusPlanned, DestinationID: "camp:blockhead"},
			wantRetryable: true,
		},
		{
			name:   "delivered",
			events: []meetinglog.Event{plan, ended(now), routed},
			want: RouteStatus{Status: RouteStatusRouted, DestinationID: "camp:blockhead",
				Type: TypeCampaign, Detail: "notes/meetings/x.md", At: now},
		},
		{
			name:   "failed twice, still retryable",
			events: []meetinglog.Event{plan, ended(now), failed, failed},
			want: RouteStatus{Status: RouteStatusFailed, DestinationID: "camp:blockhead",
				Type: TypeCampaign, Detail: "camp: no such campaign", At: now, Attempts: 2},
			wantRetryable: true,
		},
		{
			name:   "failed to the cap",
			events: []meetinglog.Event{plan, ended(now), failed, failed, failed},
			want: RouteStatus{Status: RouteStatusFailed, DestinationID: "camp:blockhead",
				Type: TypeCampaign, Detail: "camp: no such campaign", At: now, Attempts: 3},
		},
		{
			name:   "planned but still recording",
			events: []meetinglog.Event{plan},
			want:   RouteStatus{Status: RouteStatusPlanned, DestinationID: "camp:blockhead"},
		},
		{
			name:   "planned but too old to sweep",
			events: []meetinglog.Event{plan, ended(now.Add(-SweepWindow - time.Hour))},
			want:   RouteStatus{Status: RouteStatusPlanned, DestinationID: "camp:blockhead"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			events := append([]meetinglog.Event{started(now, "Weekly sync", "mac")}, tt.events...)
			writeBundle(t, dir, "weekly-sync-20260816-101500.meeting", events)

			entries := indexAll(t, dir)
			if len(entries) != 1 || entries[0].Route == nil {
				t.Fatalf("entries = %+v, want one with a route", entries)
			}
			got := *entries[0].Route
			want := tt.want
			want.Retryable = tt.wantRetryable
			if !got.At.Equal(want.At) {
				t.Errorf("route.at = %s, want %s", got.At, want.At)
			}
			got.At, want.At = time.Time{}, time.Time{}
			if got != want {
				t.Errorf("route = %+v, want %+v", got, want)
			}
		})
	}
}

// TestIndexRouteStatusMatchesTheSweep keeps the index honest against the retry
// loop it advertises: a bundle the index calls retryable is exactly one
// SweepPendingRoutes picks up.
func TestIndexRouteStatusMatchesTheSweep(t *testing.T) {
	dir := t.TempDir()
	out := t.TempDir()
	pending := planBundle(t, dir, "pending-20260816-101500.meeting", "docs", true)
	planBundle(t, dir, "unplanned-20260816-101600.meeting", "", true)

	before := indexAll(t, dir)
	retryable := map[string]bool{}
	for _, entry := range before {
		retryable[entry.ID] = entry.Route != nil && entry.Route.Retryable
	}
	if !retryable["pending-20260816-101500.meeting"] {
		t.Fatal("a bundle with a plan and no delivery is not marked retryable")
	}
	if retryable["unplanned-20260816-101600.meeting"] {
		t.Fatal("a bundle with no plan is marked retryable")
	}

	results := SweepPendingRoutes(context.Background(), sweepTestRouter(out), dir)
	if len(results) != 1 || results[0].Bundle != pending {
		t.Fatalf("sweep results = %+v, want only the pending bundle", results)
	}

	for _, entry := range indexAll(t, dir) {
		if entry.ID != "pending-20260816-101500.meeting" {
			continue
		}
		if entry.Route == nil || entry.Route.Status != RouteStatusRouted {
			t.Fatalf("route after the sweep = %+v, want routed", entry.Route)
		}
		if entry.Route.Retryable {
			t.Error("a delivered bundle is still marked retryable")
		}
	}
}

// TestIndexCacheRereadsOnlyOnChange pins the cache key: an event stream whose
// (modtime, size) is unchanged is served from the parsed entry, and any change
// to either re-reads it.
func TestIndexCacheRereadsOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	bundle := writeBundle(t, dir, "cached-20260816-101500.meeting",
		[]meetinglog.Event{started(now, "First", ""), ended(now.Add(time.Minute))})
	events := bundleEventsPath(bundle)
	info, err := os.Stat(events)
	if err != nil {
		t.Fatal(err)
	}

	cache := NewCache()
	first, _, err := cache.Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Description != "First" {
		t.Fatalf("first read = %+v", first)
	}

	// Same size, same mtime, different bytes: the cache must not notice, which
	// is what proves it did not re-read the file.
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(events, []byte(strings.Replace(string(raw), "First", "Sec0d", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(events, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	cached, _, err := cache.Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cached[0].Description != "First" {
		t.Fatalf("description = %q, want the cached %q", cached[0].Description, "First")
	}

	// A real append changes the size, so the next read parses again.
	if err := AppendRoutedEvent(events, Receipt{DestinationID: "docs", Type: TypeFile, Outcome: OutcomeRouted, Detail: "/tmp/x.md", At: now}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := cache.Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fresh[0].Description != "Sec0d" {
		t.Errorf("description = %q, want the re-read %q", fresh[0].Description, "Sec0d")
	}
	if fresh[0].Route == nil || fresh[0].Route.Status != RouteStatusRouted {
		t.Errorf("route = %+v, want routed after the append", fresh[0].Route)
	}
}

func TestCacheWarmPreparsesAndForgetsDeletedBundles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	bundle := writeBundle(t, dir, "warm-20260816-101500.meeting",
		[]meetinglog.Event{started(now, "Warm", ""), ended(now.Add(time.Minute))})

	cache := NewCache()
	if err := cache.Warm(context.Background(), dir, DefaultIndexLimit); err != nil {
		t.Fatalf("Warm() error = %v", err)
	}
	if _, ok := cache.Entry(context.Background(), bundle); !ok {
		t.Fatal("Warm() did not cache the bundle")
	}

	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	entries, _, err := cache.Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none after the bundle was deleted", entries)
	}
	if _, ok := cache.Entry(context.Background(), bundle); ok {
		t.Error("the cache still holds a deleted bundle")
	}
}

// TestIndexReadsWhatTheRecorderWrites round-trips a bundle produced by the
// real writer, so the index is pinned to the recorder's output rather than to
// a hand-built event stream.
func TestIndexReadsWhatTheRecorderWrites(t *testing.T) {
	dir := t.TempDir()
	bundle := planBundle(t, dir, "standup-20260816-101500.meeting", "docs", true)

	entries := indexAll(t, dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Description != "Standup" || got.Notes != 1 || got.State != BundleStateReady {
		t.Fatalf("entry = %+v, want the recorder's description, one note, ready", got)
	}
	if got.Bundle != bundle || got.EventsFile != bundleEventsPath(bundle) {
		t.Fatalf("paths = %q / %q, want the bundle's own", got.Bundle, got.EventsFile)
	}
	if got.Route == nil || got.Route.Status != RouteStatusPlanned || got.Route.DestinationID != "docs" {
		t.Fatalf("route = %+v, want planned for docs", got.Route)
	}
}
