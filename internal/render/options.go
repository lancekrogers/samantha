// Package render is the batch narration runtime: it turns documents into audio
// files and a manifest, separately from the live voice pipeline. Orchestration
// here is cgo-free and testable; TTS synthesis and WAV writing are injected by
// the command layer.
package render

import (
	"fmt"
	"path/filepath"
	"strings"

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
)

// Options describes one `samantha render` invocation.
type Options struct {
	Input       string  // positional input path or URL (empty with Stdin)
	Stdin       bool    // read input text from stdin
	Format      Format  // input format (auto-detected when FormatAuto)
	Out         string  // single-file output path
	OutDir      string  // multi-file output directory (with a manifest)
	Voice       string  // override the configured TTS voice
	Speed       float64 // override the configured speech speed (0 = use config)
	Title       string  // override the document title
	Manifest    string  // manifest output path (default: OUT_DIR/manifest.json for multi-file)
	JSON        bool    // print a machine-readable summary
	Resume      bool    // skip completed manifest entries with matching text hash
	Overwrite   bool    // replace existing outputs
	AudioFormat string  // optional compressed output (mp3|m4b|...); WAV is always written
	EncoderBin  string  // external encoder binary (default: ffmpeg)
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
	case FormatAuto, FormatText, FormatMarkdown, FormatHTML, FormatURL, FormatEPUB:
	default:
		return fmt.Errorf("render: unsupported --format %q", o.Format)
	}
	if o.Stdin && (o.Format == FormatURL || o.Format == FormatEPUB) {
		return fmt.Errorf("render: --format %s cannot read from --stdin", o.Format)
	}

	// Cross-check the resolved format against the output mode so a mismatch fails
	// fast here, before any TTS model is loaded. EPUB is multi-file only;
	// Markdown/HTML/URL accept either --out (single file) or --out-dir (sectioned);
	// plain text remains single-file only.
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
	if !encoder.Supported(o.AudioFormat) {
		return fmt.Errorf("render: unsupported --audio-format %q (try one of: mp3, m4a, m4b, aac, opus)", o.AudioFormat)
	}
	return nil
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
	case FormatMarkdown, FormatHTML, FormatURL, FormatEPUB:
		return true
	default:
		return false
	}
}
