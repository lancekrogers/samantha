package narrate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lancekrogers/samantha/internal/textclean"
)

// PageText is one extracted PDF page.
type PageText struct {
	Page int
	Text string
}

// Extractor extracts paginated text from a digital PDF.
type Extractor interface {
	ExtractPages(ctx context.Context, path string) (pages []PageText, warnings []string, err error)
}

// Runner runs an external command. Tests inject fakes; production uses exec.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, err error)

// PDFExtractor extracts digital PDF text via pdftotext.
type PDFExtractor struct {
	// LookPath locates the binary (default: exec.LookPath).
	LookPath func(string) (string, error)
	// Run executes the binary (default: exec.CommandContext).
	Run Runner
	// Binary defaults to "pdftotext".
	Binary string
}

// ExtractPages runs pdftotext -layout and splits form-feed pages.
func (e PDFExtractor) ExtractPages(ctx context.Context, path string) ([]PageText, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	look := e.LookPath
	if look == nil {
		look = exec.LookPath
	}
	bin := e.Binary
	if bin == "" {
		bin = "pdftotext"
	}
	if _, err := look(bin); err != nil {
		return nil, nil, fmt.Errorf("narrate: %s not found on PATH; install Poppler (e.g. brew install poppler) and re-run 'samantha doctor'", bin)
	}
	run := e.Run
	if run == nil {
		run = defaultRunner
	}
	// -layout preserves reading order; "-" writes to stdout.
	out, err := run(ctx, bin, "-layout", path, "-")
	if err != nil {
		return nil, nil, fmt.Errorf("narrate: pdftotext %s: %w", path, err)
	}
	rawPages := bytes.Split(out, []byte{'\f'})
	var pages []PageText
	var totalChars int
	for i, raw := range rawPages {
		text := textclean.CleanPrintArtifacts(string(raw))
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		totalChars += len(text)
		pages = append(pages, PageText{Page: i + 1, Text: text})
	}
	if len(pages) == 0 {
		return nil, nil, fmt.Errorf("narrate: no extractable text in %s (scanned PDFs need OCR; try a digital PDF or 'samantha narrate plan' after OCR)", path)
	}
	var warnings []string
	// Rough density heuristic: very short multi-page PDFs are OCR-suspect.
	avg := totalChars / len(pages)
	if avg < 80 {
		warnings = append(warnings, fmt.Sprintf("low text density (~%d chars/page); OCR-suspect — prefer 'samantha narrate plan' with prompt cleanup", avg))
	}
	return pages, warnings, nil
}

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// FormatPageCount is a small helper for tests/messages.
func FormatPageCount(n int) string {
	return strconv.Itoa(n)
}
