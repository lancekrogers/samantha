// Package audiobook defines the human-reviewable audiobook production plan.
package audiobook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lancekrogers/samantha/internal/narrate"
	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"gopkg.in/yaml.v3"
)

// SchemaVersion is the versioned audiobook-specific production-plan schema.
const SchemaVersion = "samantha.audiobook-plan.v1"

const (
	DecisionInclude = "include"
	DecisionExclude = "exclude"
	DecisionReview  = "review"
)

var validKinds = map[string]bool{
	"front_matter": true,
	"main_content": true,
	"reference":    true,
	"index":        true,
	"back_matter":  true,
	"navigation":   true,
	"unknown":      true,
}

var (
	contentsLine = regexp.MustCompile(`(?i)^\s*[^.!?]{1,100}\.{2,}\s*\d+\s*$`)
	pageLine     = regexp.MustCompile(`(?i)^\s*[^.!?]{1,100}\s+\d+\s*$`)
)

// Plan is the source of truth for what an audiobook build will narrate.
type Plan struct {
	Schema   string     `yaml:"schema"`
	Source   Source     `yaml:"source"`
	Defaults Defaults   `yaml:"defaults"`
	Sections []Section  `yaml:"sections"`
	Review   Review     `yaml:"review,omitempty"`
	Render   RenderInfo `yaml:"render,omitempty"`
}

type Source struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256,omitempty"`
	Format string `yaml:"format"`
	Title  string `yaml:"title,omitempty"`
	Author string `yaml:"author,omitempty"`
}

type Defaults struct {
	Decision    string `yaml:"decision"`
	AudioFormat string `yaml:"audio_format,omitempty"`
	KeepWAV     bool   `yaml:"keep_wav"`
}

type Section struct {
	ID                  string `yaml:"id"`
	Order               int    `yaml:"order"`
	Title               string `yaml:"title,omitempty"`
	Kind                string `yaml:"kind"`
	Suggestion          string `yaml:"suggestion"`
	Decision            string `yaml:"decision"`
	DecisionReason      string `yaml:"decision_reason,omitempty"`
	Confidence          string `yaml:"confidence,omitempty"`
	SourceRef           string `yaml:"source_ref,omitempty"`
	ExtractedPath       string `yaml:"extracted_path"`
	ExtractedSHA256     string `yaml:"extracted_sha256,omitempty"`
	EstimatedChars      int    `yaml:"estimated_chars"`
	EstimatedDurationMS int64  `yaml:"estimated_duration_ms"`
	AudioPath           string `yaml:"audio_path,omitempty"`
	Status              string `yaml:"status"`
}

type Review struct {
	ReviewedAt string `yaml:"reviewed_at,omitempty"`
	Reviewer   string `yaml:"reviewer,omitempty"`
	Notes      string `yaml:"notes,omitempty"`
}

type RenderInfo struct {
	Voice string  `yaml:"voice,omitempty"`
	Speed float64 `yaml:"speed,omitempty"`
}

type PlanOptions struct {
	Input     string
	OutDir    string
	Format    render.Format
	Overwrite bool
}

type PlanResult struct {
	Plan     *Plan
	PlanPath string
	MDPath   string
}

// BuildPlan extracts the source using the existing narration planner, then
// adds audiobook classification and explicit human decisions. It writes no
// audio and does not load TTS assets.
func BuildPlan(ctx context.Context, opts PlanOptions) (PlanResult, error) {
	if err := ctx.Err(); err != nil {
		return PlanResult{}, err
	}
	if strings.TrimSpace(opts.Input) == "" {
		return PlanResult{}, fmt.Errorf("audiobook plan: provide an input path")
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		return PlanResult{}, fmt.Errorf("audiobook plan: provide --out-dir DIR")
	}
	format := opts.Format
	if format == "" || format == render.FormatAuto {
		format = (render.Options{Input: opts.Input}).ResolveFormat()
	}
	if format != render.FormatEPUB && format != render.FormatPDF {
		return PlanResult{}, fmt.Errorf("audiobook plan: only EPUB or PDF input is supported")
	}
	planPath := filepath.Join(opts.OutDir, "production-plan.yaml")
	mdPath := filepath.Join(opts.OutDir, "production-plan.md")
	if !opts.Overwrite {
		if _, err := os.Stat(planPath); err == nil {
			return PlanResult{}, fmt.Errorf("audiobook plan: %s exists (use --overwrite)", planPath)
		} else if !os.IsNotExist(err) {
			return PlanResult{}, fmt.Errorf("audiobook plan: stat %s: %w", planPath, err)
		}
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return PlanResult{}, fmt.Errorf("audiobook plan: create output dir: %w", err)
	}

	// BuildPlan is deliberately layered over narrate.BuildPlan so EPUB/PDF
	// extraction stays in one place. The generic YAML is an internal bridge;
	// the user-facing artifact is the audiobook plan below.
	bridgePath := filepath.Join(opts.OutDir, ".narration-plan.yaml")
	defer os.Remove(bridgePath)
	res, err := narrate.BuildPlan(ctx, narrate.PlanOptions{
		Input: opts.Input, Out: bridgePath, Format: format, Overwrite: true,
	}, narrate.PDFExtractor{})
	if err != nil {
		return PlanResult{}, err
	}
	title, author := sourceMetadata(opts.Input, format)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(opts.Input), filepath.Ext(opts.Input))
	}
	p := &Plan{
		Schema:   SchemaVersion,
		Source:   Source{Path: opts.Input, SHA256: res.Plan.Source.SHA256, Format: string(format), Title: title, Author: author},
		Defaults: Defaults{Decision: DecisionReview, AudioFormat: "m4b", KeepWAV: true},
		Sections: make([]Section, 0, len(res.Plan.Sections)),
	}
	for i, raw := range res.Plan.Sections {
		path := resolvePath(filepath.Dir(bridgePath), raw.ExtractedPath)
		data, err := os.ReadFile(path)
		if err != nil {
			return PlanResult{}, fmt.Errorf("audiobook plan: read extracted section %s: %w", raw.ID, err)
		}
		text := strings.TrimSpace(string(data))
		kind, suggestion, confidence, reason := classify(raw.Title, text)
		decision := suggestion
		if decision == "" {
			decision = DecisionReview
		}
		p.Sections = append(p.Sections, Section{
			ID: raw.ID, Order: i + 1, Title: raw.Title, Kind: kind,
			Suggestion: suggestion, Decision: decision, DecisionReason: reason,
			Confidence: confidence, SourceRef: raw.SourceRef,
			ExtractedPath: raw.ExtractedPath, ExtractedSHA256: raw.ExtractedSHA256,
			EstimatedChars: len([]rune(text)), EstimatedDurationMS: estimateDurationMS(text),
			AudioPath: filepath.Join("audio", render.Slugify(raw.Title)+".wav"), Status: "pending",
		})
	}
	if err := p.Save(planPath); err != nil {
		return PlanResult{}, err
	}
	if err := p.WriteMarkdown(mdPath); err != nil {
		return PlanResult{}, err
	}
	return PlanResult{Plan: p, PlanPath: planPath, MDPath: mdPath}, nil
}

func sourceMetadata(input string, format render.Format) (title, author string) {
	if format != render.FormatEPUB {
		return "", ""
	}
	zr, err := zip.OpenReader(input)
	if err != nil {
		return "", ""
	}
	defer zr.Close()
	book, err := epub.Parse(&zr.Reader)
	if err != nil {
		return "", ""
	}
	return book.Metadata.Title, book.Metadata.Author
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func estimateDurationMS(text string) int64 {
	words := len(strings.Fields(text))
	if words == 0 {
		return 0
	}
	return int64(float64(words) / 150.0 * 60_000)
}

func classify(title, text string) (kind, suggestion, confidence, reason string) {
	lower := strings.ToLower(strings.TrimSpace(title))
	if strings.Contains(lower, "table of contents") || strings.Contains(lower, "contents") {
		return "navigation", DecisionExclude, "high", "title identifies navigation content"
	}
	if strings.Contains(lower, "index") || looksLikeIndex(text) {
		return "index", DecisionExclude, "high", "title or line shape identifies an index"
	}
	if anyPhrase(lower, "title page", "copyright", "dedication", "epigraph", "colophon") {
		return "front_matter", DecisionExclude, "high", "title identifies publishing front matter"
	}
	if strings.Contains(lower, "conclusion") {
		return "main_content", DecisionInclude, "high", "conclusion contains substantive prose"
	}
	if anyPhrase(lower, "acknowledg", "thanks", "about the author", "author bio") {
		return "back_matter", DecisionReview, "medium", "back matter is a human preference"
	}
	if anyPhrase(lower, "appendix", "glossary", "bibliography", "references", "cheatsheet") {
		return "reference", DecisionReview, "medium", "reference material may or may not belong in the audiobook"
	}
	if strings.TrimSpace(text) == "" {
		return "unknown", DecisionReview, "low", "section has no extracted narration"
	}
	return "main_content", DecisionInclude, "high", "section contains substantive prose"
}

func anyPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func looksLikeIndex(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 4 {
		return false
	}
	shape := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if contentsLine.MatchString(line) || pageLine.MatchString(line) {
			shape++
		}
	}
	return shape >= 3 && shape*2 >= len(lines)
}

// Load reads and strictly validates an audiobook plan.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading audiobook plan: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var p Plan
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing audiobook plan: document is empty")
		}
		return nil, fmt.Errorf("parsing audiobook plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid audiobook plan: %w", err)
	}
	return &p, nil
}

func (p *Plan) Validate() error {
	if p.Schema != SchemaVersion {
		return fmt.Errorf("schema must be %q, got %q", SchemaVersion, p.Schema)
	}
	if p.Source.Path == "" || p.Source.Format == "" {
		return fmt.Errorf("source.path and source.format are required")
	}
	if p.Defaults.Decision != DecisionReview && p.Defaults.Decision != DecisionInclude && p.Defaults.Decision != DecisionExclude {
		return fmt.Errorf("defaults.decision must be include, exclude, or review")
	}
	if len(p.Sections) == 0 {
		return fmt.Errorf("sections must not be empty")
	}
	seen := map[string]bool{}
	for i, s := range p.Sections {
		if s.ID == "" || seen[s.ID] {
			return fmt.Errorf("sections[%d]: unique id is required", i)
		}
		seen[s.ID] = true
		if s.Order != i+1 {
			return fmt.Errorf("sections[%d]: order must be %d", i, i+1)
		}
		if !validKinds[s.Kind] {
			return fmt.Errorf("sections[%d]: unknown kind %q", i, s.Kind)
		}
		if !validDecision(s.Decision) || !validDecision(s.Suggestion) {
			return fmt.Errorf("sections[%d]: decision and suggestion must be include, exclude, or review", i)
		}
		if s.ExtractedPath == "" {
			return fmt.Errorf("sections[%d]: extracted_path is required", i)
		}
	}
	return nil
}

func validDecision(v string) bool {
	return v == DecisionInclude || v == DecisionExclude || v == DecisionReview
}

func (p *Plan) Save(path string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid audiobook plan: %w", err)
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding audiobook plan: %w", err)
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	committed = true
	return nil
}

func (p *Plan) WriteMarkdown(path string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid audiobook plan: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Audiobook production plan\n\n")
	fmt.Fprintf(&b, "- Source: %s\n- Format: %s\n- Sections: %d\n\n", p.Source.Path, p.Source.Format, len(p.Sections))
	b.WriteString("Review each row before rendering. `review` decisions block production.\n\n")
	b.WriteString("| # | Decision | Suggestion | Kind | Title | Est. time | Reason |\n")
	b.WriteString("|---:|---|---|---|---|---:|---|\n")
	for _, s := range p.Sections {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n", s.Order, s.Decision, s.Suggestion, s.Kind, markdownCell(s.Title), formatDuration(s.EstimatedDurationMS), markdownCell(s.DecisionReason))
	}
	return writeAtomic(path, []byte(b.String()))
}

func markdownCell(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), "|", "\\|"), "\n", " ")
}

func formatDuration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	minutes := ms / 60_000
	seconds := (ms % 60_000) / 1_000
	return fmt.Sprintf("%dm %02ds", minutes, seconds)
}

// ApplyDecisions applies explicit include/exclude edits and returns an error
// for unknown IDs or conflicting edits.
func (p *Plan) ApplyDecisions(includes, excludes []string, reason string) error {
	seen := map[string]string{}
	for _, id := range includes {
		if id == "" {
			continue
		}
		if prior, ok := seen[id]; ok && prior != DecisionInclude {
			return fmt.Errorf("section %q appears in both include and exclude", id)
		}
		seen[id] = DecisionInclude
	}
	for _, id := range excludes {
		if id == "" {
			continue
		}
		if prior, ok := seen[id]; ok && prior != DecisionExclude {
			return fmt.Errorf("section %q appears in both include and exclude", id)
		}
		seen[id] = DecisionExclude
	}
	for id, decision := range seen {
		found := false
		for i := range p.Sections {
			if p.Sections[i].ID != id {
				continue
			}
			found = true
			p.Sections[i].Decision = decision
			if strings.TrimSpace(reason) != "" {
				p.Sections[i].DecisionReason = reason
			}
		}
		if !found {
			return fmt.Errorf("unknown section ID %q", id)
		}
	}
	return p.Validate()
}

func (p *Plan) Unresolved() []Section {
	var unresolved []Section
	for _, s := range p.Sections {
		if s.Decision == DecisionReview {
			unresolved = append(unresolved, s)
		}
	}
	return unresolved
}

// PlanPath returns the conventional plan path under an audiobook output dir.
func PlanPath(outDir string) string { return filepath.Join(outDir, "production-plan.yaml") }

// HashText is exported for consumers that need to compare edited extracted
// text with the plan identity.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
