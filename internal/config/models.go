package config

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/textclean"
)

// ModelFile describes a required model file (individual download).
type ModelFile struct {
	Name   string
	URL    string
	Size   int64  // expected size in bytes (0 = skip check)
	SHA256 string // expected hex checksum (empty = skip check)
}

// ModelArchive describes a tar.bz2 archive to download and extract.
type ModelArchive struct {
	Name       string   // display name for progress
	URL        string   // tar.bz2 URL
	SHA256     string   // expected hex checksum of compressed archive (empty = skip check)
	TargetDir  string   // extraction target directory (defaults to ModelsDir)
	CheckFiles []string // paths relative to TargetDir to verify extraction
}

// AssetRequest describes which runtime assets are needed for a command.
type AssetRequest struct {
	NeedSTT bool
	NeedTTS bool
	NeedVAD bool
}

// EnsureRuntimeAssets downloads any missing model files and archives needed for
// this run. The required asset set is resolved once from the asset manifest
// (ManifestFor), so URLs, file names, and extraction targets have a single
// source of truth; this function only performs the downloads.
func EnsureRuntimeAssets(cfg *Config, req AssetRequest, onProgress func(name string, pct float64)) error {
	manifest, err := ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	dir := ModelsDir()
	if err := ensureManifest(manifest, dir, onProgress); err != nil {
		return err
	}
	if req.NeedTTS && strings.EqualFold(cfg.TTSProvider, "kokoro") {
		if err := sanitizeKokoroLexicons(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not sanitize kokoro lexicons: %v\n", err)
		}
	}
	return nil
}

// ensureManifest downloads every missing file and archive in the manifest into
// dir. It is the parameterized core of EnsureRuntimeAssets, so it can be tested
// against a temp dir and a fake HTTP server. Already-present files and
// already-extracted archives are skipped (no re-download).
func ensureManifest(manifest AssetManifest, dir string, onProgress func(name string, pct float64)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	// Individual file downloads.
	for _, m := range manifest.ModelFiles() {
		path := filepath.Join(dir, m.Name)
		if fileExists(path, m.Size) {
			continue
		}

		if onProgress != nil {
			onProgress(m.Name, 0)
		}

		if err := downloadFile(path, m.URL, m.Name, m.Size, m.SHA256, func(pct float64) {
			if onProgress != nil {
				onProgress(m.Name, pct)
			}
		}); err != nil {
			return err
		}
	}

	// Archive downloads with extraction.
	for _, a := range manifest.ModelArchives(dir) {
		targetDir := dir
		if a.TargetDir != "" {
			targetDir = a.TargetDir
		}

		if archiveExtracted(targetDir, a.CheckFiles) {
			continue
		}

		if onProgress != nil {
			onProgress(a.Name, 0)
		}

		if err := downloadAndExtractArchive(targetDir, a.URL, a.Name, a.CheckFiles, a.SHA256, func(pct float64) {
			if onProgress != nil {
				onProgress(a.Name, pct)
			}
		}); err != nil {
			return err
		}
	}

	return nil
}

// EnsureModels preserves the old behavior for callers that still need the default asset set.
func EnsureModels(cfg *Config, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssets(cfg, DefaultAssetRequest(cfg), onProgress)
}

// archiveExtracted checks if all expected files/dirs exist.
func archiveExtracted(dir string, checkFiles []string) bool {
	for _, f := range checkFiles {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func sanitizeKokoroLexicons(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "lexicon*.txt"))
	if err != nil {
		return err
	}

	for _, path := range paths {
		if err := sanitizeKokoroLexicon(path); err != nil {
			return err
		}
	}

	return nil
}

func sanitizeKokoroLexicon(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	cleaned := textclean.StripUnsupportedKokoroMarks(string(data))
	if cleaned == string(data) {
		return nil
	}

	return os.WriteFile(path, []byte(cleaned), info.Mode().Perm())
}

// downloadAndExtractArchive downloads a tar.bz2 and extracts to dir,
// stripping the top-level directory prefix.
// downloadAndExtractArchive downloads a tar.bz2 from url and extracts it into
// dir. The archive is decompressed and handed to extractTarStream, which
// extracts into a temp dir, verifies the check files, then atomically promotes
// the result — so a partial or malicious archive never lands at dir. name labels
// the asset in error messages.
func downloadAndExtractArchive(dir, url, name string, checkFiles []string, sha256Hex string, onProgress func(float64)) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d for %s", name, resp.StatusCode, url)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create target dir: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, ".archive-*.tar.bz2.part")
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	hasher := sha256.New()
	var sink io.Writer = tmp
	if sha256Hex != "" {
		sink = io.MultiWriter(tmp, hasher)
	}

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return fmt.Errorf("download %s: %w", name, werr)
			}
			written += int64(n)
			if total > 0 && onProgress != nil {
				onProgress(float64(written) / float64(total) * 100)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download %s: %w", name, readErr)
		}
	}
	if sha256Hex != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, sha256Hex) {
			return fmt.Errorf("download %s: checksum mismatch, got %s want %s", name, got, sha256Hex)
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}

	return extractTarStream(bzip2.NewReader(tmp), dir, name, checkFiles)
}

// extractTarStream extracts a decompressed tar stream into dir. It extracts into
// a temp directory inside dir, verifies that every check file is present, then
// promotes each top-level entry into dir, so dir is never left with a partial or
// rejected extraction. It is the testable core (no bzip2/HTTP).
func extractTarStream(r io.Reader, dir, name string, checkFiles []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	tmpDir, err := os.MkdirTemp(dir, ".extract-*")
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpDir)
		}
	}()

	if err := extractTar(tar.NewReader(r), tmpDir, name); err != nil {
		return err
	}
	if !archiveExtracted(tmpDir, checkFiles) {
		return fmt.Errorf("extract %s: archive missing expected files after extraction", name)
	}

	// Promote each top-level entry into the final directory, replacing any stale
	// partial from a previous run.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	for _, e := range entries {
		src := filepath.Join(tmpDir, e.Name())
		dst := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("extract %s: clear %s: %w", name, e.Name(), err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("extract %s: promote %s: %w", name, e.Name(), err)
		}
	}

	committed = true
	os.RemoveAll(tmpDir)
	return nil
}

// extractTar extracts every entry of tr into dir, stripping the archive's
// top-level directory prefix. It rejects unsafe paths (absolute or escaping the
// root) and link entries (symlinks/hardlinks), and ignores other special types.
func extractTar(tr *tar.Reader, dir, name string) error {
	var prefix string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("extract %s: reading archive: %w", name, err)
		}

		// Reject absolute entry names before any prefix stripping can mask them.
		if strings.HasPrefix(header.Name, "/") || filepath.IsAbs(header.Name) {
			return fmt.Errorf("extract %s: refusing absolute path %q in archive", name, header.Name)
		}

		// Detect the top-level prefix from the first entry, then strip it.
		if prefix == "" {
			if parts := strings.SplitN(header.Name, "/", 2); len(parts) > 1 {
				prefix = parts[0] + "/"
			}
		}
		rel := strings.TrimPrefix(header.Name, prefix)
		if rel == "" || rel == "." {
			continue
		}

		switch header.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("extract %s: refusing link entry %q", name, rel)
		}

		target, err := safeJoin(dir, rel)
		if err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("extract %s: create dir %s: %w", name, rel, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("extract %s: create parent dir for %s: %w", name, rel, err)
			}
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("extract %s: create file %s: %w", name, rel, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("extract %s: write file %s: %w", name, rel, err)
			}
			f.Close()
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("extract %s: chmod %s: %w", name, rel, err)
			}
		}
	}
	return nil
}

// safeJoin joins rel under dir, rejecting absolute paths and any path that would
// escape dir via "..".
func safeJoin(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe absolute path %q in archive", rel)
	}
	target := filepath.Join(dir, rel)
	within, err := filepath.Rel(dir, target)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes archive root", rel)
	}
	return target, nil
}

func fileExists(path string, expectedSize int64) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return false
	}
	return true
}

// downloadFile downloads url into path. It writes to a temp file in the target
// directory, verifies the expected size and checksum when known, and atomically
// renames into place on success. The temp file is removed on any failure, so a
// partial or corrupt download never lands at path. file names the asset in
// error messages.
func downloadFile(path, url, file string, size int64, sha256Hex string, onProgress func(float64)) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", file, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d for %s", file, resp.StatusCode, url)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create parent dir: %w", file, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	var sink io.Writer = tmp
	if sha256Hex != "" {
		sink = io.MultiWriter(tmp, hasher)
	}

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if total > 0 && onProgress != nil {
				onProgress(float64(written) / float64(total) * 100)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download %s: %w", file, readErr)
		}
	}

	if size > 0 && written != size {
		return fmt.Errorf("download %s: size mismatch, got %d bytes want %d", file, written, size)
	}
	if sha256Hex != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, sha256Hex) {
			return fmt.Errorf("download %s: checksum mismatch, got %s want %s", file, got, sha256Hex)
		}
	}

	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
