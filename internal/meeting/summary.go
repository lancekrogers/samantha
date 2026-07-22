package meeting

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// LoadSummaryFromJSONL rebuilds a Summary from a meeting bundle's internal
// event stream. Used by `meeting route` and route-later.
func LoadSummaryFromJSONL(jsonlPath string) (meetinglog.Summary, error) {
	bundle := bundlePathForJSONL(jsonlPath)
	if bundle == "" {
		return meetinglog.Summary{}, fmt.Errorf("meeting: events must be inside a .meeting bundle: %s", jsonlPath)
	}
	events, err := ReadEvents(jsonlPath)
	if err != nil {
		return meetinglog.Summary{}, err
	}
	s := meetinglog.Summary{
		JSONLFile: jsonlPath,
		File:      filepath.Join(bundle, meetinglog.BundleDocumentName),
		Bundle:    bundle,
	}
	for _, e := range events {
		switch e.Type {
		case meetinglog.TypeSessionStart:
			s.Description = e.Desc
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
				s.StartedAt = t
			}
		case meetinglog.TypeSessionEnd:
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
				s.EndedAt = t
			}
		case meetinglog.TypeUtterance:
			s.Utterances++
		case meetinglog.TypeNote:
			s.Notes++
		case meetinglog.TypeBookmark:
			s.Bookmarks++
		case meetinglog.TypeError:
			s.Errors++
		case meetinglog.TypeSpeakerAnalysis:
			s.SpeakerStatus = e.Status
			s.SpeakerCount = e.SpeakerCount
			s.SpeakerAnalysisFile = e.Artifact
			s.AudioFile = e.AudioFile
			s.SpeakerError = e.Message
		}
	}
	if s.Description == "" {
		s.Description = "meeting"
	}
	if !s.StartedAt.IsZero() && !s.EndedAt.IsZero() {
		s.DurationSeconds = int64(s.Duration().Round(time.Second) / time.Second)
	}
	return s, nil
}

func bundlePathForJSONL(jsonlPath string) string {
	if filepath.Base(jsonlPath) != meetinglog.BundleEventsName {
		return ""
	}
	internal := filepath.Dir(jsonlPath)
	if filepath.Base(internal) != meetinglog.BundleInternalDirName {
		return ""
	}
	bundle := filepath.Dir(internal)
	if !strings.HasSuffix(strings.ToLower(filepath.Base(bundle)), ".meeting") {
		return ""
	}
	return bundle
}

func bundleEventsPath(bundle string) string {
	return filepath.Join(bundle, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName)
}

// ResolveMeetingFile picks a meeting artifact path:
// - empty → most recent meeting bundle's event stream under meetingsDir
// - .meeting directory → its hidden event stream
// - meeting.md → its bundle event stream
func ResolveMeetingFile(meetingsDir, path string) (jsonlPath string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return mostRecentMeetingEvents(meetingsDir)
	}
	// Expand ~ for convenience.
	path = expandHome(path)
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("meeting: meeting file: %w", err)
	}
	if st.IsDir() {
		if !strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".meeting") {
			return "", fmt.Errorf("meeting: expected a .meeting bundle, got directory %s", path)
		}
		candidate := bundleEventsPath(path)
		if info, statErr := os.Stat(candidate); statErr != nil || info.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("is a directory")
			}
			return "", fmt.Errorf("meeting: bundle events missing for %s: %w", path, statErr)
		}
		return candidate, nil
	}
	if filepath.Base(path) == meetinglog.BundleDocumentName &&
		strings.HasSuffix(strings.ToLower(filepath.Base(filepath.Dir(path))), ".meeting") {
		candidate := bundleEventsPath(filepath.Dir(path))
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("meeting: bundle events missing for %s: %w", path, err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("meeting: expected a .meeting bundle or its meeting.md, got %s", path)
}

func mostRecentMeetingEvents(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("meeting: list meetings dir: %w", err)
	}
	type hit struct {
		path    string
		started time.Time
	}
	var hits []hit
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".meeting") {
			continue
		}
		candidate := bundleEventsPath(filepath.Join(dir, e.Name()))
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			hits = append(hits, hit{path: candidate, started: meetingRecordedAt(candidate, info.ModTime())})
		}
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("meeting: no .meeting bundles in %s", dir)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].started.Equal(hits[j].started) {
			// Same-second recordings use the sortable bundle path as a
			// deterministic tie-breaker instead of mutable mtime.
			return hits[i].path > hits[j].path
		}
		return hits[i].started.After(hits[j].started)
	})
	return hits[0].path, nil
}

// meetingRecordedAt uses the immutable session_start timestamp rather than
// file mtime. Routing appends an event later and must not make an old meeting
// look like the newest recording.
func meetingRecordedAt(jsonlPath string, fallback time.Time) time.Time {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fallback
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return fallback
	}
	var event meetinglog.Event
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Type != meetinglog.TypeSessionStart {
		return fallback
	}
	started, err := time.Parse(time.RFC3339, event.TS)
	if err != nil {
		return fallback
	}
	return started
}
