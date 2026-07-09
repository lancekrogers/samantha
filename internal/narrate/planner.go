package narrate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/render"
	"github.com/lancekrogers/samantha/internal/render/epub"
	"github.com/lancekrogers/samantha/internal/render/extractors"
)

// PlanOptions controls narrate plan generation.
type PlanOptions struct {
	Input      string
	Out        string // plan YAML path
	ExtractDir string // default: <plan-dir>/extracted
	Overwrite  bool
	Format     render.Format // auto when empty
}

// PlanResult is the outcome of BuildPlan.
type PlanResult struct {
	Plan         *Plan
	PlanPath     string
	SectionCount int
	Format       string
	Warnings     []string
}

// BuildPlan extracts structured sections from Markdown, HTML, EPUB, or PDF and
// writes a narration plan plus extracted text files.
func BuildPlan(ctx context.Context, opts PlanOptions, pdf Extractor) (PlanResult, error) {
	if err := ctx.Err(); err != nil {
		return PlanResult{}, err
	}
	if strings.TrimSpace(opts.Input) == "" {
		return PlanResult{}, fmt.Errorf("narrate plan: provide an input path")
	}
	if strings.TrimSpace(opts.Out) == "" {
		return PlanResult{}, fmt.Errorf("narrate plan: provide --out PATH")
	}
	if !opts.Overwrite {
		if _, err := os.Stat(opts.Out); err == nil {
			return PlanResult{}, fmt.Errorf("narrate plan: %s exists (use --overwrite)", opts.Out)
		} else if !os.IsNotExist(err) {
			return PlanResult{}, fmt.Errorf("narrate plan: stat %s: %w", opts.Out, err)
		}
	}

	format := resolvePlanFormat(opts.Input, opts.Format)
	srcHash, err := fileSHA256(opts.Input)
	if err != nil {
		return PlanResult{}, err
	}

	extractDir := opts.ExtractDir
	if extractDir == "" {
		extractDir = filepath.Join(filepath.Dir(opts.Out), "extracted")
	}
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return PlanResult{}, fmt.Errorf("narrate plan: create extract dir: %w", err)
	}

	var (
		sections    []Section
		method      = ExtractMethodNative
		warnings    []string
		extractOpts *ExtractOptions
	)

	switch format {
	case render.FormatMarkdown, render.FormatHTML:
		units, err := structuredUnits(opts.Input, format)
		if err != nil {
			return PlanResult{}, err
		}
		sections, err = writeExtractedSections(extractDir, units)
		if err != nil {
			return PlanResult{}, err
		}
	case render.FormatEPUB:
		units, err := epubUnits(opts.Input)
		if err != nil {
			return PlanResult{}, err
		}
		sections, err = writeExtractedSections(extractDir, units)
		if err != nil {
			return PlanResult{}, err
		}
	case render.FormatPDF:
		if pdf == nil {
			return PlanResult{}, fmt.Errorf("narrate plan: PDF extraction is not configured")
		}
		method = ExtractMethodPDFToText
		extractOpts = &ExtractOptions{Layout: true}
		pages, warns, err := pdf.ExtractPages(ctx, opts.Input)
		if err != nil {
			return PlanResult{}, err
		}
		warnings = append(warnings, warns...)
		units := make([]render.RenderUnit, 0, len(pages))
		for _, p := range pages {
			id := fmt.Sprintf("page-%03d", p.Page)
			title := fmt.Sprintf("Page %d", p.Page)
			units = append(units, render.RenderUnit{ID: id, Title: title, Text: p.Text, SourceRef: opts.Input})
		}
		sections, err = writeExtractedSections(extractDir, units)
		if err != nil {
			return PlanResult{}, err
		}
		for i := range sections {
			page := pages[i].Page
			sections[i].SourceRange = &SourceRange{Pages: []int{page, page}}
		}
	default:
		return PlanResult{}, fmt.Errorf("narrate plan: unsupported format %q (try markdown, html, epub, or pdf)", format)
	}

	if len(sections) == 0 {
		return PlanResult{}, fmt.Errorf("narrate plan: no extractable sections in %s", opts.Input)
	}

	// Relativize paths when possible for portable plans.
	planDir := filepath.Dir(opts.Out)
	for i := range sections {
		sections[i].ExtractedPath = relPrefer(planDir, sections[i].ExtractedPath)
		sections[i].PreparedPath = filepath.Join("prepared", sections[i].ID+".md")
		sections[i].AudioPath = filepath.Join("audio", sections[i].ID+".wav")
	}

	plan := &Plan{
		Schema: SchemaVersion,
		Source: Source{
			Path:   opts.Input,
			SHA256: srcHash,
			Format: string(format),
		},
		Extract: Extract{
			Method:  method,
			Options: extractOpts,
		},
		Sections: sections,
	}
	if err := plan.Save(opts.Out); err != nil {
		return PlanResult{}, err
	}
	return PlanResult{
		Plan:         plan,
		PlanPath:     opts.Out,
		SectionCount: len(sections),
		Format:       string(format),
		Warnings:     warnings,
	}, nil
}

func resolvePlanFormat(input string, explicit render.Format) render.Format {
	if explicit != "" && explicit != render.FormatAuto {
		return explicit
	}
	return (render.Options{Input: input, Format: render.FormatAuto}).ResolveFormat()
}

func structuredUnits(path string, format render.Format) ([]render.RenderUnit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("narrate plan: read %s: %w", path, err)
	}
	var doc render.Document
	switch format {
	case render.FormatMarkdown:
		doc, err = extractors.ExtractMarkdown(path, data)
	case render.FormatHTML:
		doc, err = extractors.ExtractHTML(path, data)
	default:
		return nil, fmt.Errorf("narrate plan: internal: bad structured format %s", format)
	}
	if err != nil {
		return nil, err
	}
	return doc.Units(), nil
}

func epubUnits(path string) ([]render.RenderUnit, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("narrate plan: open epub %s: %w", path, err)
	}
	defer zr.Close()
	book, err := epub.Parse(&zr.Reader)
	if err != nil {
		return nil, err
	}
	units := make([]render.RenderUnit, 0, len(book.Chapters))
	for _, ch := range book.Chapters {
		data, err := book.ReadChapter(ch.Href)
		if err != nil {
			return nil, err
		}
		doc, err := extractors.ExtractHTML(ch.Href, data)
		if err != nil {
			return nil, err
		}
		title := ch.Title
		if title == "" {
			title = doc.Title
		}
		units = append(units, render.RenderUnit{ID: ch.ID, Title: title, Text: doc.Narration(), SourceRef: ch.Href})
	}
	return units, nil
}

func writeExtractedSections(dir string, units []render.RenderUnit) ([]Section, error) {
	sections := make([]Section, 0, len(units))
	for _, u := range units {
		id := u.ID
		if id == "" {
			id = "sec-" + render.Slugify(u.Title)
		}
		if id == "" || id == "sec-" {
			id = fmt.Sprintf("sec-%03d", len(sections)+1)
		}
		// Sanitize path component.
		safe := render.Slugify(id)
		if safe == "" {
			safe = fmt.Sprintf("sec-%03d", len(sections)+1)
		}
		path := filepath.Join(dir, safe+".txt")
		text := strings.TrimSpace(u.Text)
		if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
			return nil, fmt.Errorf("narrate plan: write %s: %w", path, err)
		}
		sum := sha256.Sum256([]byte(text + "\n"))
		sections = append(sections, Section{
			ID:              id,
			Title:           u.Title,
			ExtractedPath:   path,
			ExtractedSHA256: hex.EncodeToString(sum[:]),
		})
	}
	return sections, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("narrate plan: hash %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func relPrefer(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
