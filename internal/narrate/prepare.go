package narrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/prompts"
)

// PrepareOptions controls narrate prepare.
type PrepareOptions struct {
	PlanPath      string
	SystemPrompt  string
	StylePrompt   string
	Pronunciation string
	Profile       string
	OutDir        string
	Resume        bool
	Overwrite     bool
	JSON          bool
	// Batch is required; inject PassThroughBatch or a real BatchProvider.
	Batch brain.BatchProvider
	// ProfileLoader resolves --profile NAME (optional).
	ProfileLoader func(name string) (system, style, pronunciation string, err error)
}

// PrepareResult summarizes prepare.
type PrepareResult struct {
	Prepared int
	Skipped  int
	Failed   int
	PlanPath string
	Provider string
	Model    string
}

// Prepare transforms each extracted section through the batch brain.
func Prepare(ctx context.Context, opts PrepareOptions) (PrepareResult, error) {
	if opts.Batch == nil {
		return PrepareResult{}, fmt.Errorf("narrate prepare: batch provider is required")
	}
	plan, err := Load(opts.PlanPath)
	if err != nil {
		return PrepareResult{}, err
	}
	base := filepath.Dir(opts.PlanPath)

	sysPath, stylePath, pronPath, err := resolvePromptPaths(opts)
	if err != nil {
		return PrepareResult{}, err
	}
	sysText, sysHash, err := loadPromptFile(sysPath)
	if err != nil {
		return PrepareResult{}, err
	}
	styleText, styleHash, err := loadPromptFile(stylePath)
	if err != nil {
		return PrepareResult{}, err
	}
	pronText, pronHash, err := loadPromptFile(pronPath)
	if err != nil {
		return PrepareResult{}, err
	}

	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join(base, "prepared")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return PrepareResult{}, fmt.Errorf("narrate prepare: mkdir: %w", err)
	}

	var prevPrompts Prompts
	if plan.Prompts != nil {
		prevPrompts = *plan.Prompts
	}
	promptsUnchanged := prevPrompts.SystemSHA256 == sysHash &&
		prevPrompts.StyleSHA256 == styleHash &&
		prevPrompts.PronunciationSHA256 == pronHash

	if plan.Prompts == nil {
		plan.Prompts = &Prompts{}
	}
	plan.Prompts.System = relPrefer(base, sysPath)
	plan.Prompts.SystemSHA256 = sysHash
	plan.Prompts.Style = relPrefer(base, stylePath)
	plan.Prompts.StyleSHA256 = styleHash
	plan.Prompts.Pronunciation = relPrefer(base, pronPath)
	plan.Prompts.PronunciationSHA256 = pronHash

	var res PrepareResult
	res.PlanPath = opts.PlanPath
	var firstErr error

	for i := range plan.Sections {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		sec := &plan.Sections[i]
		extracted := resolveAgainst(base, sec.ExtractedPath)
		textBytes, err := os.ReadFile(extracted)
		if err != nil {
			return res, fmt.Errorf("narrate prepare: read %s: %w", extracted, err)
		}
		textHash := sha256Hex(textBytes)
		extractedUnchanged := sec.ExtractedSHA256 == textHash
		sec.ExtractedSHA256 = textHash

		outPath := filepath.Join(outDir, sanitizeID(sec.ID)+".md")
		sec.PreparedPath = relPrefer(base, outPath)

		// Resume: skip only when the prepared file exists AND the full identity
		// is unchanged — extracted text hash, all prompt hashes, and a recorded
		// provider/model. Any changed input re-prepares this section.
		if opts.Resume && !opts.Overwrite && extractedUnchanged && promptsUnchanged {
			if prev, err := os.ReadFile(outPath); err == nil && len(prev) > 0 {
				if sec.PreparedProvider != "" && sec.PreparedModel != "" {
					res.Skipped++
					continue
				}
			}
		}

		result, err := opts.Batch.Transform(ctx, brain.BatchRequest{
			SystemPrompt:  sysText,
			StylePrompt:   styleText,
			Pronunciation: pronText,
			SectionTitle:  sec.Title,
			Text:          string(textBytes),
			Metadata:      map[string]string{"section_id": sec.ID},
		})
		if err != nil {
			res.Failed++
			firstErr = fmt.Errorf("narrate prepare: section %s: %w", sec.ID, err)
			break
		}
		if err := os.WriteFile(outPath, []byte(strings.TrimSpace(result.Text)+"\n"), 0o644); err != nil {
			return res, fmt.Errorf("narrate prepare: write %s: %w", outPath, err)
		}
		sec.PreparedProvider = result.Provider
		sec.PreparedModel = result.Model
		res.Prepared++
		res.Provider = result.Provider
		res.Model = result.Model
	}

	if err := plan.Save(opts.PlanPath); err != nil {
		return res, err
	}
	return res, firstErr
}

func resolvePromptPaths(opts PrepareOptions) (sys, style, pron string, err error) {
	sys, style, pron = opts.SystemPrompt, opts.StylePrompt, opts.Pronunciation
	if opts.Profile != "" {
		if opts.ProfileLoader == nil {
			return "", "", "", fmt.Errorf("narrate prepare: --profile requires a profile loader")
		}
		ps, pst, pp, err := opts.ProfileLoader(opts.Profile)
		if err != nil {
			return "", "", "", err
		}
		if sys == "" {
			sys = ps
		}
		if style == "" {
			style = pst
		}
		if pron == "" {
			pron = pp
		}
	}
	// Empty prompts are allowed (pass-through still works).
	return sys, style, pron, nil
}

func loadPromptFile(path string) (text, hash string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("narrate prepare: read prompt %s: %w", path, err)
	}
	// Prefer structured prompt assembly when the file is prompt YAML.
	if doc, perr := prompts.Load(data); perr == nil && doc != nil {
		assembled := strings.TrimSpace(doc.Assemble())
		return assembled, sha256Hex([]byte(assembled)), nil
	}
	text = string(data)
	return text, sha256Hex(data), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func resolveAgainst(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func sanitizeID(id string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, id)
	return strings.Trim(s, "-")
}
