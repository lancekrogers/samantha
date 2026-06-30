package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RenderSchema is the render manifest schema version.
const RenderSchema = "samantha.render.v1"

// Segment statuses.
const (
	StatusComplete = "complete"
	StatusSkipped  = "skipped"
	StatusFailed   = "failed"
)

// ManifestSegment records one rendered segment.
type ManifestSegment struct {
	Index      int    `json:"index"`
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	TextSHA256 string `json:"text_sha256"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
}

// RenderManifest is the inspectable record of a render, written as
// manifest.json. It is required for multi-file renders and recommended for
// single-file renders.
type RenderManifest struct {
	Schema       string            `json:"schema"`
	CreatedAt    string            `json:"created_at,omitempty"`
	Title        string            `json:"title,omitempty"`
	Source       string            `json:"source"`
	SourceFormat Format            `json:"source_format"`
	Voice        string            `json:"voice,omitempty"`
	SpeechSpeed  float64           `json:"speech_speed,omitempty"`
	SampleRate   int               `json:"sample_rate"`
	Segments     []ManifestSegment `json:"segments"`
}

// Counts returns the number of complete, skipped, and failed segments.
func (m RenderManifest) Counts() (complete, skipped, failed int) {
	for _, s := range m.Segments {
		switch s.Status {
		case StatusComplete:
			complete++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}
	return
}

// TotalDurationMS sums the segment durations.
func (m RenderManifest) TotalDurationMS() int64 {
	var total int64
	for _, s := range m.Segments {
		total += s.DurationMS
	}
	return total
}

// WriteManifest writes m to path as indented JSON, creating parent directories.
func WriteManifest(path string, m RenderManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	return nil
}

// textHash returns the hex SHA-256 of a segment's text, used for resume.
func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// sourceLabel returns a stable source identifier for the manifest.
func sourceLabel(opts Options) string {
	if opts.Stdin {
		return "stdin"
	}
	return opts.Input
}
