// Package render is the batch narration runtime: it turns documents into audio
// files and a manifest, separately from the live voice pipeline. Orchestration
// here is cgo-free and testable; TTS synthesis and WAV writing are injected by
// the command layer.
package render

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/internal/render/encoder"
)

// Format identifies the input document format.
type Format string

const (
	FormatAuto     Format = "auto"
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
	FormatURL      Format = "url"
	FormatEPUB     Format = "epub"
	FormatPDF      Format = "pdf"
)

// Options describes one `samantha render` invocation.
type Options struct {
	Input    string  // positional input path or URL (empty with Stdin)
	Stdin    bool    // read input text from stdin
	Format   Format  // input format (auto-detected when FormatAuto)
	Out      string  // single-file output path
	OutDir   string  // multi-file output directory (with a manifest)
	Voice    string  // override the configured TTS voice
	Language string  // override the configured Qwen synthesis language
	Speed    float64 // override the configured speech speed (0 = use config)
	// TTS* fields are resolved provider metadata populated by the command
	// layer. They are written to manifests without storing private reference
	// audio or transcript content.
	TTSProvider                  string
	TTSModel                     string
	TTSWorker                    string
	TTSMode                      string
	TTSVoice                     string
	TTSLanguage                  string
	TTSInstructionSHA256         string
	TTSReferenceAudioSHA256      string
	TTSReferenceTranscriptSHA256 string
	Title                        string // override the document title
	Manifest                     string // manifest output path (default: OUT_DIR/manifest.json for multi-file)
	JSON                         bool   // print a machine-readable summary
	Resume                       bool   // skip completed manifest entries with matching text hash
	Overwrite                    bool   // replace existing outputs
	AudioFormat                  string // optional compressed output (mp3|m4b|...); WAV is always written
	EncoderBin                   string // external encoder binary (default: ffmpeg)
	MaxSegmentChars              int    // TTS text segment cap; 0 means defaultMaxSegmentChars
	PauseHeading                 string // pause after headings (Go duration; empty/0 = none)
	PauseParagraph               string // pause after paragraphs (Go duration; empty/0 = none)
	CodeBlocks                   string // markdown code-block policy: skip|read (default skip)
}

// ManifestPath returns where the manifest should be written. Every render writes
// an inspectable manifest: --manifest overrides the path; multi-file renders
// default to OUT_DIR/manifest.json; single-file renders default to a
// "<out>.manifest.json" sidecar.
func (o Options) ManifestPath() string {
	if o.Manifest != "" {
		return o.Manifest
	}
	if o.MultiFile() {
		return filepath.Join(o.OutDir, "manifest.json")
	}
	if o.Out != "" {
		return o.Out + ".manifest.json"
	}
	return ""
}

// Validate checks the option combination before any synthesis. It is cgo-free
// and touches neither the network nor the filesystem, so it can fail fast on a
// bad invocation.
func (o Options) Validate() error {
	if o.Stdin && strings.TrimSpace(o.Input) != "" {
		return fmt.Errorf("render: cannot combine --stdin with an input argument")
	}
	if !o.Stdin && strings.TrimSpace(o.Input) == "" {
		return fmt.Errorf("render: provide an input path/URL or --stdin")
	}

	if o.Out == "" && o.OutDir == "" {
		return fmt.Errorf("render: provide --out FILE or --out-dir DIR")
	}
	if o.Out != "" && o.OutDir != "" {
		return fmt.Errorf("render: --out and --out-dir are mutually exclusive")
	}

	switch o.Format {
	case FormatAuto, FormatText, FormatMarkdown, FormatHTML, FormatURL, FormatEPUB, FormatPDF:
	default:
		return fmt.Errorf("render: unsupported --format %q", o.Format)
	}
	if o.Stdin && (o.Format == FormatURL || o.Format == FormatEPUB || o.Format == FormatPDF) {
		return fmt.Errorf("render: --format %s cannot read from --stdin", o.Format)
	}

	// Cross-check the resolved format against the output mode so a mismatch fails
	// fast here, before any TTS model is loaded. EPUB is multi-file only;
	// Markdown/HTML/URL/PDF accept either --out (single file) or --out-dir (sectioned);
	// plain text remains single-file only. EPUB requires --out-dir.
	format := o.ResolveFormat()
	switch {
	case format == FormatEPUB && o.Out != "":
		return fmt.Errorf("render: --format epub writes multiple files; use --out-dir DIR")
	case format == FormatText && o.OutDir != "":
		return fmt.Errorf("render: --format text writes a single file; use --out FILE")
	case !supportsSectionedOutDir(format) && o.OutDir != "":
		return fmt.Errorf("render: --format %s writes a single file; use --out FILE", format)
	}

	if o.Speed < 0 {
		return fmt.Errorf("render: --speed must be >= 0, got %v", o.Speed)
	}
	if o.MaxSegmentChars != 0 && o.MaxSegmentChars < minSegmentChars {
		return fmt.Errorf("render: --max-segment-chars must be 0 (default) or >= %d, got %d", minSegmentChars, o.MaxSegmentChars)
	}
	if _, err := parsePauseDuration("--pause-heading", o.PauseHeading); err != nil {
		return err
	}
	if _, err := parsePauseDuration("--pause-paragraph", o.PauseParagraph); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(o.CodeBlocks)) {
	case "", "skip", "read":
	default:
		return fmt.Errorf("render: unsupported --code-blocks %q (try skip or read)", o.CodeBlocks)
	}
	if !encoder.Supported(o.AudioFormat) {
		return fmt.Errorf("render: unsupported --audio-format %q (try one of: mp3, m4a, m4b, aac, opus)", o.AudioFormat)
	}
	return nil
}

// EffectiveMaxSegmentChars returns the TTS segment cap used for this render.
// Zero means the package default.
func (o Options) EffectiveMaxSegmentChars() int {
	if o.MaxSegmentChars > 0 {
		return o.MaxSegmentChars
	}
	return defaultMaxSegmentChars
}

// EffectiveCodeBlocks returns the markdown code-block policy (default skip).
func (o Options) EffectiveCodeBlocks() string {
	v := strings.ToLower(strings.TrimSpace(o.CodeBlocks))
	if v == "" {
		return "skip"
	}
	return v
}

// minSegmentChars is the practical floor for --max-segment-chars when set.
const minSegmentChars = 100

// parsePauseDuration parses an optional Go duration for pause flags.
// Empty means no pause (duration 0). Negative durations are rejected.
func parsePauseDuration(flag, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("render: invalid %s %q: %w", flag, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("render: %s must be >= 0, got %s", flag, raw)
	}
	return d, nil
}

// PauseHeadingDuration returns the configured heading pause (0 if unset).
func (o Options) PauseHeadingDuration() time.Duration {
	d, _ := parsePauseDuration("--pause-heading", o.PauseHeading)
	return d
}

// PauseParagraphDuration returns the configured paragraph pause (0 if unset).
func (o Options) PauseParagraphDuration() time.Duration {
	d, _ := parsePauseDuration("--pause-paragraph", o.PauseParagraph)
	return d
}

// ResolveFormat returns the effective format, inferring it from the input when
// Format is auto/empty.
func (o Options) ResolveFormat() Format {
	if o.Format != FormatAuto && o.Format != "" {
		return o.Format
	}
	if o.Stdin {
		return FormatText
	}
	lower := strings.ToLower(strings.TrimSpace(o.Input))
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return FormatURL
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return FormatMarkdown
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return FormatHTML
	case strings.HasSuffix(lower, ".epub"):
		return FormatEPUB
	case strings.HasSuffix(lower, ".pdf"):
		return FormatPDF
	default:
		return FormatText
	}
}

// MultiFile reports whether this render writes multiple files (and thus a
// manifest) rather than a single audio file.
func (o Options) MultiFile() bool { return o.OutDir != "" }

// supportsSectionedOutDir reports whether format may write multi-file sectioned
// output under --out-dir (in addition to single-file --out where applicable).
func supportsSectionedOutDir(f Format) bool {
	switch f {
	case FormatMarkdown, FormatHTML, FormatURL, FormatEPUB, FormatPDF:
		return true
	default:
		return false
	}
}
