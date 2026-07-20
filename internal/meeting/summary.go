package meeting

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/meetinglog"
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
	ext := filepath.Ext(jsonlPath)
	base := strings.TrimSuffix(jsonlPath, ext)
	candidate := base + ".log"
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate
	}
	return candidate
}

// ResolveMeetingFile picks a meeting artifact path:
// - empty → most recent .jsonl (or .log) under meetingsDir
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
		return mostRecentJSONL(path)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		return path, nil
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
		return "", fmt.Errorf("meeting: expected .log or .jsonl, got %s", path)
	}
}

func mostRecentJSONL(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("meeting: list meetings dir: %w", err)
	}
	type hit struct {
		path string
		mod  time.Time
	}
	var hits []hit
	for _, e := range entries {
		if e.IsDir() {
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
		hits = append(hits, hit{path: filepath.Join(dir, name), mod: info.ModTime()})
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("meeting: no meeting .jsonl files in %s", dir)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].mod.After(hits[j].mod) })
	return hits[0].path, nil
}
