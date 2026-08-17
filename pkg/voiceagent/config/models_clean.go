package config

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CleanCategory classifies what a candidate is, so a caller can tell a
// leftover from a real model before it offers to delete anything.
type CleanCategory string

const (
	// CleanCategoryJunk is an interrupted download or extraction the
	// installer left behind — removing it costs nothing.
	CleanCategoryJunk CleanCategory = "junk"
	// CleanCategoryAsset is a real model file or directory that nothing
	// currently references. Removing it means a re-download.
	CleanCategoryAsset CleanCategory = "asset"
)

// CleanKind distinguishes a file candidate from a whole directory.
type CleanKind string

const (
	CleanKindFile CleanKind = "file"
	CleanKindDir  CleanKind = "dir"
)

// CleanCandidate is one path under the models dir that no required asset
// claims. Size is best-effort (bytes; recursive for directories, 0 when
// unknown) and never follows symlinks.
type CleanCandidate struct {
	Path     string        `json:"path"`
	Rel      string        `json:"rel"`
	Size     int64         `json:"size_bytes"`
	Category CleanCategory `json:"category"`
	Kind     CleanKind     `json:"kind"`
}

// IsDir reports whether the candidate is a directory.
func (c CleanCandidate) IsDir() bool { return c.Kind == CleanKindDir }

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
//
// It sees only the global configuration. Prefer RequiredAssetPaths plus
// RequiredSet.CleanCandidates: a manifest alone knows nothing about persona
// profiles or config keys the current mode does not load, which is how the
// 2026-08-17 clean deleted six personas' TTS runtime.
func (m AssetManifest) CleanCandidates(ctx context.Context, modelsDir string) ([]CleanCandidate, error) {
	modelsDir = filepath.Clean(modelsDir)
	set := RequiredSet{ModelsDir: modelsDir, own: m.requiredPaths(modelsDir)}
	return set.CleanCandidates(ctx)
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
	own := newOwnership()
	for _, a := range m.Assets {
		paths, suppressRoot := a.ownedPaths(modelsDir)
		for _, p := range paths {
			own.add(p, modelsDir)
		}
		own.suppressRoot = own.suppressRoot || suppressRoot
	}
	return own
}

// newOwnership returns an empty ownership map set.
func newOwnership() ownership {
	return ownership{required: map[string]bool{}, parents: map[string]bool{}}
}

// add records p as owned and every directory between it and modelsDir as a
// parent, so a required file never hides its whole containing directory.
func (o *ownership) add(p, modelsDir string) {
	o.required[p] = true
	for dir := filepath.Dir(p); len(dir) > len(modelsDir); dir = filepath.Dir(dir) {
		o.parents[dir] = true
	}
}

// ownedPaths resolves the paths one asset owns under modelsDir, following the
// ownership-by-shape rules documented on requiredPaths. suppressRoot reports a
// root-extracting archive that is installed but whose footprint cannot be
// enumerated, so no top-level entry may be reported at all.
func (a Asset) ownedPaths(modelsDir string) (paths []string, suppressRoot bool) {
	if a.IsArchive() && a.TargetDir != "" {
		// Own the whole extraction target; ancestors are marked as parents so
		// a nested target dir is still reached.
		return []string{filepath.Join(modelsDir, a.TargetDir)}, false
	}
	paths = append(paths, a.installPaths(modelsDir)...)
	if !a.IsArchive() {
		return paths, false
	}
	// Root-extracting archive.
	if a.Archive.SHA256 != "" {
		paths = append(paths, archiveInstallMarkerPath(modelsDir, a.ID))
	}
	files, hasMarker := archiveMarkerFiles(modelsDir, a.ID)
	switch {
	case len(files) > 0:
		for _, f := range files {
			paths = append(paths, filepath.Join(modelsDir, f))
		}
	case hasMarker || archiveExtracted(modelsDir, a.CheckFiles):
		// Installed but footprint unknown: be conservative.
		suppressRoot = true
	}
	return paths, suppressRoot
}

// collectCandidates walks dir without following symlinks. Entries that are
// required are skipped; directories that contain required paths are descended;
// everything else is appended as a candidate. suppressRoot applies to this level
// only (the models-dir root): when set, unclaimed entries here are not reported,
// but required-holding directories are still descended.
func collectCandidates(ctx context.Context, modelsDir, dir string, own ownership, suppressRoot bool, out *[]CleanCandidate) error {
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
			if err := collectCandidates(ctx, modelsDir, p, own, false, out); err != nil {
				return err
			}
			continue
		}
		if suppressRoot {
			continue
		}
		*out = append(*out, newCleanCandidate(modelsDir, p, entrySize(p, e), e.IsDir()))
	}
	return nil
}

// newCleanCandidate describes one unclaimed path relative to the models dir.
func newCleanCandidate(modelsDir, path string, size int64, isDir bool) CleanCandidate {
	kind := CleanKindFile
	if isDir {
		kind = CleanKindDir
	}
	rel, err := filepath.Rel(modelsDir, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	return CleanCandidate{
		Path:     path,
		Rel:      rel,
		Size:     size,
		Category: cleanCategory(filepath.Base(path)),
		Kind:     kind,
	}
}

// cleanCategory classifies a candidate by name: the installer's interrupted
// downloads (.archive-*.part) and extractions (.extract-*) are junk, and
// everything else is a real asset the caller must look at before deleting.
func cleanCategory(name string) CleanCategory {
	switch {
	case strings.HasPrefix(name, ".extract-"),
		strings.HasPrefix(name, ".archive-") && strings.HasSuffix(name, ".part"),
		strings.HasSuffix(name, ".part"):
		return CleanCategoryJunk
	default:
		return CleanCategoryAsset
	}
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
