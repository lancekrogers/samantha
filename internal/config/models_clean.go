package config

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CleanCandidate is one path under the models dir that no required asset
// claims. Size is best-effort (bytes; recursive for directories, 0 when
// unknown) and never follows symlinks.
type CleanCandidate struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"dir,omitempty"`
}

// CleanApplyResult reports the candidates removed by an apply-mode cleanup.
type CleanApplyResult struct {
	Deleted []CleanCandidate `json:"deleted"`
	Bytes   int64            `json:"bytes"`
}

// CleanCandidates lists the paths under modelsDir that are not claimed by any
// asset in the manifest, as removal candidates for `models clean`. It only
// reads the filesystem — it never deletes, never follows symlinks, and never
// reports a path outside modelsDir. A directory containing no required paths
// is reported as a single candidate; directories that hold required paths are
// descended so only their unclaimed entries are reported. A missing modelsDir
// yields no candidates.
//
// Ownership is asset-owned, not merely install-check-owned: an archive claims
// its whole extraction footprint (every file it extracts), never just the small
// CheckFiles subset, so a currently-selected archive's files are never reported
// as removable — see requiredPaths.
func (m AssetManifest) CleanCandidates(ctx context.Context, modelsDir string) ([]CleanCandidate, error) {
	modelsDir = filepath.Clean(modelsDir)
	own := m.requiredPaths(modelsDir)

	candidates := []CleanCandidate{}
	err := collectCandidates(ctx, modelsDir, own, own.suppressRoot, &candidates)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// DeleteCleanCandidates removes the exact candidate paths after re-validating
// path confinement under modelsDir. Symlinks are removed as links; their targets
// are never followed.
func DeleteCleanCandidates(ctx context.Context, modelsDir string, candidates []CleanCandidate) (CleanApplyResult, error) {
	result := CleanApplyResult{}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := validateCleanCandidatePath(modelsDir, candidate.Path); err != nil {
			return result, err
		}
		if _, err := os.Lstat(candidate.Path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return result, fmt.Errorf("models clean: stat %s: %w", candidate.Path, err)
		}
		if err := os.RemoveAll(candidate.Path); err != nil {
			return result, fmt.Errorf("models clean: remove %s: %w", candidate.Path, err)
		}
		result.Deleted = append(result.Deleted, candidate)
		result.Bytes += candidate.Size
	}
	return result, nil
}

func validateCleanCandidatePath(modelsDir, candidatePath string) error {
	modelsAbs, err := filepath.Abs(filepath.Clean(modelsDir))
	if err != nil {
		return fmt.Errorf("models clean: resolve models dir: %w", err)
	}
	candidateAbs, err := filepath.Abs(filepath.Clean(candidatePath))
	if err != nil {
		return fmt.Errorf("models clean: resolve candidate path: %w", err)
	}
	rel, err := filepath.Rel(modelsAbs, candidateAbs)
	if err != nil {
		return fmt.Errorf("models clean: compare candidate path: %w", err)
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("models clean: refusing to delete path outside models dir: %s", candidatePath)
	}
	return nil
}

// ownership records which paths under the models dir belong to a required asset.
// required paths are never reported and never descended; parents are descended
// so only their unclaimed entries surface. suppressRoot is set when a
// root-extracting archive is installed but its footprint cannot be enumerated
// (a legacy marker), so top-level entries must not be reported at all rather
// than risk flagging a file the archive owns.
type ownership struct {
	required     map[string]bool
	parents      map[string]bool
	suppressRoot bool
}

// requiredPaths resolves everything the manifest's assets own under modelsDir.
// Ownership by shape:
//   - Individual files: the file paths themselves.
//   - Archive with a TargetDir: the ENTIRE target directory — the archive
//     extracts more than its CheckFiles (e.g. both quantized and full-precision
//     encoder/joiner), and all of it belongs to the selected asset.
//   - Archive extracting to the models-dir root: its CheckFiles, its install
//     marker, and every top-level entry recorded in that marker at extraction.
//     An installed root archive whose marker predates the recorded footprint
//     can't be enumerated, so suppressRoot conservatively hides all top-level
//     entries instead of risking a false positive.
func (m AssetManifest) requiredPaths(modelsDir string) ownership {
	own := ownership{required: map[string]bool{}, parents: map[string]bool{}}
	add := func(p string) {
		own.required[p] = true
		for dir := filepath.Dir(p); len(dir) > len(modelsDir); dir = filepath.Dir(dir) {
			own.parents[dir] = true
		}
	}
	for _, a := range m.Assets {
		if a.IsArchive() && a.TargetDir != "" {
			// Own the whole extraction target; ancestors are marked as parents so
			// a nested target dir is still reached.
			add(filepath.Join(modelsDir, a.TargetDir))
			continue
		}
		for _, p := range a.installPaths(modelsDir) {
			add(p)
		}
		if !a.IsArchive() {
			continue
		}
		// Root-extracting archive.
		if a.Archive.SHA256 != "" {
			add(archiveInstallMarkerPath(modelsDir, a.ID))
		}
		files, hasMarker := archiveMarkerFiles(modelsDir, a.ID)
		switch {
		case len(files) > 0:
			for _, f := range files {
				add(filepath.Join(modelsDir, f))
			}
		case hasMarker || archiveExtracted(modelsDir, a.CheckFiles):
			// Installed but footprint unknown: be conservative.
			own.suppressRoot = true
		}
	}
	return own
}

// collectCandidates walks dir without following symlinks. Entries that are
// required are skipped; directories that contain required paths are descended;
// everything else is appended as a candidate. suppressRoot applies to this level
// only (the models-dir root): when set, unclaimed entries here are not reported,
// but required-holding directories are still descended.
func collectCandidates(ctx context.Context, dir string, own ownership, suppressRoot bool, out *[]CleanCandidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("models clean: read %s: %w", dir, err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if own.required[p] {
			continue
		}
		// e.IsDir() is false for symlinks, so a symlinked directory is never
		// descended — it can only be a candidate itself.
		if own.parents[p] && e.IsDir() {
			if err := collectCandidates(ctx, p, own, false, out); err != nil {
				return err
			}
			continue
		}
		if suppressRoot {
			continue
		}
		*out = append(*out, CleanCandidate{Path: p, Size: entrySize(p, e), IsDir: e.IsDir()})
	}
	return nil
}

// entrySize returns the best-effort size in bytes of a candidate: the lstat
// size for files and symlinks, the recursive file total for directories.
// Symlinks are never followed, so linked-to content is never counted.
func entrySize(p string, e fs.DirEntry) int64 {
	if !e.IsDir() {
		info, err := e.Info()
		if err != nil {
			return 0
		}
		return info.Size()
	}
	var total int64
	// Best-effort size scan: the callback already ignores per-entry errors, so
	// a failed walk simply yields the partial total accumulated so far.
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
