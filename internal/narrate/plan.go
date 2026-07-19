// Package narrate defines the samantha.narration-plan.v1 document: the typed,
// validated artifact shared by the narrate pipeline stages (plan, prepare,
// render).
package narrate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the exact schema identifier a plan document must declare.
const SchemaVersion = "samantha.narration-plan.v1"

// Extraction methods. Native covers formats whose text is read directly
// (plain text, markdown, epub) with no external extraction tool.
const (
	ExtractMethodNative    = "native"
	ExtractMethodPDFToText = "pdftotext"
)

var extractMethods = []string{ExtractMethodNative, ExtractMethodPDFToText}

// Plan is the root of a narration plan document. Hash and identity fields
// (sha256 sums, prepared provider/model) are part of the v1 schema even
// though the planning stage leaves them empty; later stages fill them in
// without a schema bump.
type Plan struct {
	Schema   string    `yaml:"schema"`
	Source   Source    `yaml:"source"`
	Extract  Extract   `yaml:"extract"`
	Sections []Section `yaml:"sections,omitempty"`
	Prompts  *Prompts  `yaml:"prompts,omitempty"`
	Render   *Render   `yaml:"render,omitempty"`
}

// Source identifies the input document.
type Source struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256,omitempty"`
	Format string `yaml:"format,omitempty"`
}

// Extract selects how text is pulled out of the source.
type Extract struct {
	Method  string          `yaml:"method"`
	Options *ExtractOptions `yaml:"options,omitempty"`
}

// ExtractOptions carries method-specific extraction flags.
type ExtractOptions struct {
	Layout bool `yaml:"layout,omitempty"`
}

// Section is one narration unit: where it lives in the source and where each
// pipeline stage reads and writes its artifact.
type Section struct {
	ID               string       `yaml:"id"`
	Title            string       `yaml:"title,omitempty"`
	SourceRef        string       `yaml:"source_ref,omitempty"`
	SourceRange      *SourceRange `yaml:"source_range,omitempty"`
	ExtractedPath    string       `yaml:"extracted_path"`
	ExtractedSHA256  string       `yaml:"extracted_sha256,omitempty"`
	PreparedPath     string       `yaml:"prepared_path"`
	PreparedProvider string       `yaml:"prepared_provider,omitempty"`
	PreparedModel    string       `yaml:"prepared_model,omitempty"`
	AudioPath        string       `yaml:"audio_path"`
}

// SourceRange locates a section inside the source document. Pages is the
// inclusive [start, end] page pair for paginated formats.
type SourceRange struct {
	Pages []int `yaml:"pages,omitempty"`
}

// Prompts are the prompt files driving the prepare stage, with sha256 sums
// recorded once resolved so prepared output can be invalidated on change.
type Prompts struct {
	System              string `yaml:"system,omitempty"`
	SystemSHA256        string `yaml:"system_sha256,omitempty"`
	Style               string `yaml:"style,omitempty"`
	StyleSHA256         string `yaml:"style_sha256,omitempty"`
	Pronunciation       string `yaml:"pronunciation,omitempty"`
	PronunciationSHA256 string `yaml:"pronunciation_sha256,omitempty"`
}

// Render holds TTS output settings.
type Render struct {
	Voice       string  `yaml:"voice,omitempty"`
	Speed       float64 `yaml:"speed,omitempty"`
	AudioFormat string  `yaml:"audio_format,omitempty"`
}

// Load reads and validates a plan file.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading narration plan: %w", err)
	}
	return Parse(data)
}

// Parse strictly decodes a plan document, rejecting unknown keys, and
// validates it.
func Parse(data []byte) (*Plan, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var p Plan
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing narration plan: document is empty")
		}
		return nil, fmt.Errorf("parsing narration plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid narration plan: %w", err)
	}
	return &p, nil
}

// Save validates the plan and writes it to path, creating parent directories.
func (p *Plan) Save(path string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid narration plan: %w", err)
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding narration plan: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing narration plan: %w", err)
	}
	return nil
}

// Validate checks the document against the v1 schema rules and returns the
// first problem found.
func (p *Plan) Validate() error {
	if p.Schema != SchemaVersion {
		return fmt.Errorf("schema must be %q, got %q", SchemaVersion, p.Schema)
	}
	if p.Source.Path == "" {
		return fmt.Errorf("source.path is required")
	}
	valid := strings.Join(extractMethods, ", ")
	if p.Extract.Method == "" {
		return fmt.Errorf("extract.method is required (valid: %s)", valid)
	}
	if !slices.Contains(extractMethods, p.Extract.Method) {
		return fmt.Errorf("unknown extract.method %q (valid: %s)", p.Extract.Method, valid)
	}
	seen := make(map[string]bool, len(p.Sections))
	for i, s := range p.Sections {
		if err := s.validate(); err != nil {
			return fmt.Errorf("sections[%d]: %w", i, err)
		}
		if seen[s.ID] {
			return fmt.Errorf("sections[%d]: duplicate section id %q", i, s.ID)
		}
		seen[s.ID] = true
	}
	if p.Render != nil && p.Render.Speed < 0 {
		return fmt.Errorf("render.speed must be >= 0, got %v", p.Render.Speed)
	}
	return nil
}

func (s Section) validate() error {
	if s.ID == "" {
		return fmt.Errorf("id is required")
	}
	if s.ExtractedPath == "" {
		return fmt.Errorf("%s: extracted_path is required", s.ID)
	}
	if s.PreparedPath == "" {
		return fmt.Errorf("%s: prepared_path is required", s.ID)
	}
	if s.AudioPath == "" {
		return fmt.Errorf("%s: audio_path is required", s.ID)
	}
	if s.SourceRange != nil {
		if len(s.SourceRange.Pages) != 2 {
			return fmt.Errorf("%s: source_range.pages must be a [start, end] pair, got %d value(s)", s.ID, len(s.SourceRange.Pages))
		}
		start, end := s.SourceRange.Pages[0], s.SourceRange.Pages[1]
		if start < 1 {
			return fmt.Errorf("%s: source_range.pages start must be >= 1, got %d", s.ID, start)
		}
		if end < start {
			return fmt.Errorf("%s: source_range.pages end (%d) must be >= start (%d)", s.ID, end, start)
		}
	}
	return nil
}
