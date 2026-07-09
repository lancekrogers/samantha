package narrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/render"
)

// RenderOptions controls narrate render.
type RenderOptions struct {
	PlanPath    string
	Resume      bool
	JSON        bool
	AudioFormat string
	Voice       string
	Speed       float64
	// OutDir overrides the audio directory (default: <plan-dir>/audio).
	OutDir string
}

// BuildRenderUnits loads prepared section text into render units.
func BuildRenderUnits(plan *Plan, baseDir string) ([]render.RenderUnit, error) {
	units := make([]render.RenderUnit, 0, len(plan.Sections))
	for _, sec := range plan.Sections {
		prep := resolveAgainst(baseDir, sec.PreparedPath)
		data, err := os.ReadFile(prep)
		if err != nil {
			return nil, fmt.Errorf("narrate render: missing prepared file %s (run 'samantha narrate prepare'): %w", prep, err)
		}
		text := strings.TrimSpace(string(data))
		units = append(units, render.RenderUnit{
			ID:        sec.ID,
			Title:     sec.Title,
			Text:      text,
			SourceRef: plan.Source.Path,
		})
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("narrate render: plan has no sections")
	}
	return units, nil
}

// RenderPlanOptions converts plan + flags into render.Options for multi-file output.
func RenderPlanOptions(plan *Plan, opts RenderOptions, baseDir string) render.Options {
	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join(baseDir, "audio")
	}
	ro := render.Options{
		Input:       plan.Source.Path,
		Format:      render.Format(plan.Source.Format),
		OutDir:      outDir,
		Resume:      opts.Resume,
		AudioFormat: opts.AudioFormat,
		JSON:        opts.JSON,
		Title:       filepath.Base(plan.Source.Path),
	}
	if plan.Render != nil {
		if opts.Voice == "" {
			ro.Voice = plan.Render.Voice
		}
		if opts.Speed == 0 {
			ro.Speed = plan.Render.Speed
		}
		if opts.AudioFormat == "" {
			ro.AudioFormat = plan.Render.AudioFormat
		}
	}
	if opts.Voice != "" {
		ro.Voice = opts.Voice
	}
	if opts.Speed > 0 {
		ro.Speed = opts.Speed
	}
	return ro
}

// EnsureRenderContext is a placeholder for future preflight; currently validates plan load.
func EnsureRenderContext(ctx context.Context, planPath string) (*Plan, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	plan, err := Load(planPath)
	if err != nil {
		return nil, "", err
	}
	return plan, filepath.Dir(planPath), nil
}
