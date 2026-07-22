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

// LoadSummaryFromJSONL rebuilds a Summary from a finished meeting's JSONL
// (and optional sibling .log path). Used by `meeting route` and route-later.
func LoadSummaryFromJSONL(jsonlPath string) (meetinglog.Summary, error) {
	events, err := ReadEvents(jsonlPath)
	if err != nil {
		return meetinglog.Summary{}, err
	}
	s := meetinglog.Summary{
		JSONLFile: jsonlPath,
		File:      logPathFor(jsonlPath),
		Bundle:    bundlePathForJSONL(jsonlPath),
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

func logPathFor(jsonlPath string) string {
	if bundle := bundlePathForJSONL(jsonlPath); bundle != "" {
		return filepath.Join(bundle, meetinglog.BundleDocumentName)
	}
	ext := filepath.Ext(jsonlPath)
	base := strings.TrimSuffix(jsonlPath, ext)
	candidate := base + ".log"
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate
	}
	markdown := base + ".md"
	if st, err := os.Stat(markdown); err == nil && !st.IsDir() {
		return markdown
	}
	return candidate
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
// - empty → most recent bundle event stream or legacy .jsonl under meetingsDir
// - .meeting directory → its hidden event stream
// - meeting.md → its bundle event stream
// - .log → sibling .jsonl
// - .jsonl → as-is
func ResolveMeetingFile(meetingsDir, path string) (jsonlPath string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return mostRecentJSONL(meetingsDir)
	}
	// Expand ~ for convenience.
	path = expandHome(path)
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("meeting: meeting file: %w", err)
	}
	if st.IsDir() {
		candidate := bundleEventsPath(path)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		return mostRecentJSONL(path)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		return path, nil
	case ".md":
		candidate := bundleEventsPath(filepath.Dir(path))
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("meeting: bundle events missing for %s: %w", path, err)
		}
		return candidate, nil
	case ".log":
		j := strings.TrimSuffix(path, filepath.Ext(path)) + ".jsonl"
		if _, err := os.Stat(j); err != nil {
			return "", fmt.Errorf("meeting: sibling jsonl missing for %s: %w", path, err)
		}
		return j, nil
	default:
		// Try treating as base path.
		if _, err := os.Stat(path + ".jsonl"); err == nil {
			return path + ".jsonl", nil
		}
		return "", fmt.Errorf("meeting: expected a .meeting bundle, meeting.md, .log, or .jsonl; got %s", path)
	}
}

func mostRecentJSONL(dir string) (string, error) {
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
		if e.IsDir() {
			candidate := bundleEventsPath(filepath.Join(dir, e.Name()))
			info, statErr := os.Stat(candidate)
			if statErr == nil && !info.IsDir() {
				hits = append(hits, hit{path: candidate, started: meetingRecordedAt(candidate, info.ModTime())})
			}
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		hits = append(hits, hit{path: path, started: meetingRecordedAt(path, info.ModTime())})
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("meeting: no meeting bundles or .jsonl files in %s", dir)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].started.Equal(hits[j].started) {
			// Older event streams only recorded whole seconds. Use the sortable
			// path as a deterministic tie-breaker instead of mutable mtime.
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
