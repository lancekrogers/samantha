package config

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CleanCandidate is one path under the models dir that no required asset
// claims. Size is best-effort (bytes; recursive for directories, 0 when
// unknown) and never follows symlinks.
type CleanCandidate struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"dir,omitempty"`
}

// CleanCandidates lists the paths under modelsDir that are not claimed by any
// asset in the manifest, as removal candidates for `models clean`. It only
// reads the filesystem — it never deletes, never follows symlinks, and never
// reports a path outside modelsDir. A directory containing no required paths
// is reported as a single candidate; directories that hold required paths are
// descended so only their unclaimed entries are reported. A missing modelsDir
// yields no candidates.
func (m AssetManifest) CleanCandidates(ctx context.Context, modelsDir string) ([]CleanCandidate, error) {
	modelsDir = filepath.Clean(modelsDir)
	required, requiredParents := m.requiredPaths(modelsDir)

	candidates := []CleanCandidate{}
	err := collectCandidates(ctx, modelsDir, required, requiredParents, &candidates)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// requiredPaths resolves every absolute path the manifest's assets claim under
// modelsDir (install paths plus archive install markers) and the set of
// ancestor directories that contain them.
func (m AssetManifest) requiredPaths(modelsDir string) (required, requiredParents map[string]bool) {
	required = map[string]bool{}
	requiredParents = map[string]bool{}
	add := func(p string) {
		required[p] = true
		for dir := filepath.Dir(p); len(dir) > len(modelsDir); dir = filepath.Dir(dir) {
			requiredParents[dir] = true
		}
	}
	for _, a := range m.Assets {
		for _, p := range a.installPaths(modelsDir) {
			add(p)
		}
		if a.IsArchive() && a.Archive.SHA256 != "" {
			target := modelsDir
			if a.TargetDir != "" {
				target = filepath.Join(modelsDir, a.TargetDir)
			}
			add(archiveInstallMarkerPath(target, a.ID))
		}
	}
	return required, requiredParents
}

// collectCandidates walks dir without following symlinks. Entries that are
// required are skipped; directories that contain required paths are descended;
// everything else is appended as a candidate.
func collectCandidates(ctx context.Context, dir string, required, requiredParents map[string]bool, out *[]CleanCandidate) error {
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
		if required[p] {
			continue
		}
		// e.IsDir() is false for symlinks, so a symlinked directory is never
		// descended — it can only be a candidate itself.
		if requiredParents[p] && e.IsDir() {
			if err := collectCandidates(ctx, p, required, requiredParents, out); err != nil {
				return err
			}
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
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
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
