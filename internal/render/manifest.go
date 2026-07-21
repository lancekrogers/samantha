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
	ResumeKey  string `json:"resume_key,omitempty"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
}

// RenderManifest is the inspectable record of a render, written as
// manifest.json. It is required for multi-file renders and recommended for
// single-file renders.
type RenderManifest struct {
	Schema                       string            `json:"schema"`
	CreatedAt                    string            `json:"created_at,omitempty"`
	Title                        string            `json:"title,omitempty"`
	Source                       string            `json:"source"`
	SourceFormat                 Format            `json:"source_format"`
	Voice                        string            `json:"voice,omitempty"`
	SpeechSpeed                  float64           `json:"speech_speed,omitempty"`
	TTSProvider                  string            `json:"tts_provider,omitempty"`
	TTSModel                     string            `json:"tts_model,omitempty"`
	TTSWorker                    string            `json:"tts_worker,omitempty"`
	TTSMode                      string            `json:"tts_mode,omitempty"`
	TTSVoice                     string            `json:"tts_voice,omitempty"`
	TTSLanguage                  string            `json:"tts_language,omitempty"`
	TTSInstructionSHA256         string            `json:"tts_instruction_sha256,omitempty"`
	TTSReferenceAudioSHA256      string            `json:"tts_reference_audio_sha256,omitempty"`
	TTSReferenceTranscriptSHA256 string            `json:"tts_reference_transcript_sha256,omitempty"`
	SampleRate                   int               `json:"sample_rate"`
	Segments                     []ManifestSegment `json:"segments"`
}

func (m *RenderManifest) applyTTSMetadata(opts Options) {
	m.TTSProvider = opts.TTSProvider
	m.TTSModel = opts.TTSModel
	m.TTSWorker = opts.TTSWorker
	m.TTSMode = opts.TTSMode
	m.TTSVoice = opts.TTSVoice
	m.TTSLanguage = opts.TTSLanguage
	m.TTSInstructionSHA256 = opts.TTSInstructionSHA256
	m.TTSReferenceAudioSHA256 = opts.TTSReferenceAudioSHA256
	m.TTSReferenceTranscriptSHA256 = opts.TTSReferenceTranscriptSHA256
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

// loadPriorManifest reads an existing manifest at path for resume decisions. A
// missing, empty, or unreadable manifest yields ok=false (resume then treats
// every segment as new).
func loadPriorManifest(path string) (RenderManifest, bool) {
	if path == "" {
		return RenderManifest{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RenderManifest{}, false
	}
	var m RenderManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return RenderManifest{}, false
	}
	return m, true
}

// WriteManifest writes m to path as indented JSON, creating parent directories.
func WriteManifest(path string, m RenderManifest) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("manifest: write %s: %w", path, err)
	}
	committed = true
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
