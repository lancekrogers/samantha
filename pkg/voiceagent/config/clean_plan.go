package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// CleanApplyResult reports what an apply actually did: what it removed, what
// it declined to remove and why, and how much space came back.
type CleanApplyResult struct {
	SchemaVersion int              `json:"schema_version"`
	Deleted       []CleanCandidate `json:"deleted"`
	Skipped       []CleanSkip      `json:"skipped"`
	BytesFreed    int64            `json:"bytes_freed"`
}

// CleanSkip is one planned path the apply refused to delete.
type CleanSkip struct {
	Path   string `json:"path"`
	Rel    string `json:"rel"`
	Reason string `json:"reason"`
}

// CleanPlanSchemaVersion is the version of the dry-run payload. Version 2
// added per-candidate size/category, the protected list with reasons, and the
// plan id that apply is gated on; version 1 was a bare candidate array.
const CleanPlanSchemaVersion = 2

// CleanPlan is the exact removal list a caller was shown, and the only thing
// an apply is allowed to act on. PlanID pins the candidate set: if the set
// changes between the dry run and the apply — a config edit, another instance,
// a finished download — the ids differ and the apply refuses.
type CleanPlan struct {
	SchemaVersion int              `json:"schema_version"`
	ModelsDir     string           `json:"models_dir"`
	Candidates    []CleanCandidate `json:"candidates"`
	Protected     []ProtectedPath  `json:"protected"`
	TotalBytes    int64            `json:"total_bytes"`
	PlanID        string           `json:"plan_id"`
}

// CleanPlan resolves the current candidates and packages them with the kept
// list and a plan id.
func (rs RequiredSet) CleanPlan(ctx context.Context) (CleanPlan, error) {
	candidates, err := rs.CleanCandidates(ctx)
	if err != nil {
		return CleanPlan{}, err
	}
	protected := rs.Protected
	if protected == nil {
		protected = []ProtectedPath{}
	}
	var total int64
	for _, c := range candidates {
		total += c.Size
	}
	return CleanPlan{
		SchemaVersion: CleanPlanSchemaVersion,
		ModelsDir:     rs.ModelsDir,
		Candidates:    candidates,
		Protected:     protected,
		TotalBytes:    total,
		PlanID:        CleanPlanID(candidates),
	}, nil
}

// CleanPlanID is the sha256 of the sorted models-dir-relative candidate paths.
// Relative paths keep the id stable when the same install is inspected through
// a different absolute root (and keep a captured fixture self-consistent);
// what the id pins is which entries under the models dir would be deleted.
func CleanPlanID(candidates []CleanCandidate) string {
	rels := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rels = append(rels, c.Rel)
	}
	sort.Strings(rels)
	sum := sha256.Sum256([]byte(strings.Join(rels, "\n")))
	return hex.EncodeToString(sum[:])
}

// PlanChangedKind is the stable error discriminator a front end branches on.
const PlanChangedKind = "plan_changed"

// PlanChangedError reports that the candidate set moved between the plan the
// caller was shown and now — a config edit, a finished download, a second
// instance. Nothing is deleted; the caller re-runs the dry run and reviews the
// new list. Its JSON form is the payload `models clean --json` prints.
type PlanChangedError struct {
	Kind          string `json:"error"`
	PlanID        string `json:"plan_id"`
	CurrentPlanID string `json:"current_plan_id"`
}

// NewPlanChangedError reports the mismatch between the caller's plan and the
// current one.
func NewPlanChangedError(planID, currentPlanID string) *PlanChangedError {
	return &PlanChangedError{Kind: PlanChangedKind, PlanID: planID, CurrentPlanID: currentPlanID}
}

func (e *PlanChangedError) Error() string {
	return fmt.Sprintf("clean: the removal list changed since it was shown (plan %s, current %s); re-run the dry run and review it again",
		shortPlanID(e.PlanID), shortPlanID(e.CurrentPlanID))
}

// shortPlanID keeps an error message readable without losing identity.
func shortPlanID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// planIDPattern matches a CleanPlanID: a sha256 in lowercase hex.
var planIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseCleanPlan reads a caller-supplied plan: either the dry-run JSON
// document or a bare plan id. Only the id gates the apply; any candidate list
// the document carries is re-validated against the current one before anything
// is removed.
func ParseCleanPlan(data []byte) (CleanPlan, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return CleanPlan{}, fmt.Errorf("clean plan: empty")
	}
	if !strings.HasPrefix(trimmed, "{") {
		if !planIDPattern.MatchString(trimmed) {
			return CleanPlan{}, fmt.Errorf("clean plan: %q is neither a dry-run document nor a plan id", trimmed)
		}
		return CleanPlan{PlanID: trimmed}, nil
	}
	var plan CleanPlan
	if err := json.Unmarshal([]byte(trimmed), &plan); err != nil {
		return CleanPlan{}, fmt.Errorf("clean plan: %w", err)
	}
	if !planIDPattern.MatchString(plan.PlanID) {
		return CleanPlan{}, fmt.Errorf("clean plan: missing plan_id")
	}
	// A document whose id does not describe its own list is not the list
	// anybody reviewed. Refuse it rather than guess which half is true.
	if id := CleanPlanID(plan.Candidates); id != plan.PlanID {
		return CleanPlan{}, fmt.Errorf("clean plan: plan_id %s does not match its candidate list (%s)",
			shortPlanID(plan.PlanID), shortPlanID(id))
	}
	return plan, nil
}

// DeleteCleanPlan removes the planned paths that are still removable right
// now. Every path is re-validated: one that is no longer a current candidate —
// a config change made it required again, another instance already handled it —
// is skipped with a reason and never deleted. Symlinks are removed as links;
// their targets are never followed. A path outside modelsDir is refused
// outright rather than skipped: a plan naming one is not a stale plan, it is a
// plan that must not be executed.
func DeleteCleanPlan(ctx context.Context, modelsDir string, planned, current []CleanCandidate) (CleanApplyResult, error) {
	removable := make(map[string]CleanCandidate, len(current))
	for _, candidate := range current {
		removable[candidate.Path] = candidate
	}
	result := CleanApplyResult{
		SchemaVersion: CleanPlanSchemaVersion,
		Deleted:       []CleanCandidate{},
		Skipped:       []CleanSkip{},
	}
	for _, want := range planned {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := validateCleanCandidatePath(modelsDir, want.Path); err != nil {
			return result, err
		}
		candidate, ok := removable[want.Path]
		if !ok {
			result.Skipped = append(result.Skipped, cleanSkip(want, "no longer a removable candidate"))
			continue
		}
		if _, err := os.Lstat(candidate.Path); os.IsNotExist(err) {
			result.Skipped = append(result.Skipped, cleanSkip(candidate, "already gone"))
			continue
		} else if err != nil {
			return result, fmt.Errorf("models clean: stat %s: %w", candidate.Path, err)
		}
		if err := os.RemoveAll(candidate.Path); err != nil {
			return result, fmt.Errorf("models clean: remove %s: %w", candidate.Path, err)
		}
		result.Deleted = append(result.Deleted, candidate)
		result.BytesFreed += candidate.Size
	}
	return result, nil
}

// cleanSkip records why one planned path was left alone.
func cleanSkip(candidate CleanCandidate, reason string) CleanSkip {
	return CleanSkip{Path: candidate.Path, Rel: candidate.Rel, Reason: reason}
}
