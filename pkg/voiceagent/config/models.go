package config

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
	"github.com/lancekrogers/samantha/pkg/voiceagent/textclean"
)

// downloadStallTimeout aborts a download whose body has gone completely quiet.
// There is deliberately no overall request deadline: model archives are hundreds
// of MB, and a fixed timeout would kill slow-but-healthy links. Connection setup
// is bounded by the transport timeouts on downloadClient instead.
const downloadStallTimeout = 60 * time.Second

// downloadClient is shared by every model download. http.DefaultClient (what a
// bare http.Get uses) has no timeouts at all, so a wedged model host could hang
// startup or render asset setup forever.
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// openDownload GETs url with ctx cancellation and stall protection: the request
// aborts when the caller cancels ctx or when the body delivers no bytes for
// downloadStallTimeout. The caller owns resp.Body and must Close it. name labels
// the asset in error messages.
func openDownload(ctx context.Context, url, name string) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("download %s: HTTP %d for %s", name, resp.StatusCode, url)
	}
	resp.Body = &stallGuard{rc: resp.Body, timer: time.AfterFunc(downloadStallTimeout, cancel), cancel: cancel}
	return resp, nil
}

// stallGuard cancels its request when Read delivers nothing for
// downloadStallTimeout; every productive read re-arms the timer. Close releases
// the timer and the request context.
type stallGuard struct {
	rc     io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.rc.Read(p)
	if n > 0 {
		g.timer.Reset(downloadStallTimeout)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	g.cancel()
	return g.rc.Close()
}

// ModelFile describes a required model file (individual download).
type ModelFile struct {
	Name   string
	URL    string
	Size   int64  // expected size in bytes (0 = skip check)
	SHA256 string // expected hex checksum (empty = skip check)
}

// ModelArchive describes a tar.bz2 archive to download and extract.
type ModelArchive struct {
	ID         string   // stable manifest asset id
	Name       string   // display name for progress
	URL        string   // tar.bz2 URL
	SHA256     string   // expected hex checksum of compressed archive (empty = skip check)
	TargetDir  string   // extraction target directory (defaults to ModelsDir)
	CheckFiles []string // paths relative to TargetDir to verify extraction
}

// AssetRequest describes which runtime assets are needed for a command.
type AssetRequest struct {
	NeedSTT     bool
	NeedTTS     bool
	NeedVAD     bool
	NeedSpeaker bool
}

// AssetProgress is one progress tick for a single asset, reported by
// EnsureRuntimeAssetsProgress. Bytes/Total are 0 when unknown (no
// Content-Length, or a phase that has no byte count of its own); Pct is
// always clamped to [0,100] regardless of how the emitting phase computed it.
type AssetProgress struct {
	Asset string  `json:"asset"`
	Phase string  `json:"phase"` // start|download|verify|extract|install|done|skipped
	Bytes int64   `json:"bytes"`
	Total int64   `json:"total"`
	Pct   float64 `json:"pct"`
}

const (
	assetPhaseStart    = "start"
	assetPhaseDownload = "download"
	assetPhaseVerify   = "verify"
	assetPhaseExtract  = "extract"
	assetPhaseInstall  = "install"
	assetPhaseDone     = "done"
	assetPhaseSkipped  = "skipped"
)

// clampPct is the single place asset progress percentages are bounded to
// [0,100]. Every emitter (manifest downloads, qwen native installs, the
// already-installed shortcuts) routes its percentage through this or
// through newAssetProgress, so a bad Content-Length or an over-eager caller
// can never produce an out-of-range tick.
func clampPct(pct float64) float64 {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// pctOfBytes computes a clamped percentage from a byte count and a total.
// It returns 0 when total is unknown (<=0).
func pctOfBytes(bytes, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return clampPct(float64(bytes) / float64(total) * 100)
}

// newAssetProgress builds an AssetProgress tick, deriving Pct from bytes/total
// and forcing it to 100 for the done/skipped phases (which report completion
// even when total is unknown). A negative total (net/http's convention for
// "no Content-Length") is normalised to 0 here, so Total is always either the
// real byte count or 0 — never a sentinel.
func newAssetProgress(asset, phase string, bytes, total int64) AssetProgress {
	if total < 0 {
		total = 0
	}
	pct := pctOfBytes(bytes, total)
	if phase == assetPhaseDone || phase == assetPhaseSkipped {
		pct = 100
	}
	return AssetProgress{Asset: asset, Phase: phase, Bytes: bytes, Total: total, Pct: pct}
}

// EnsureRuntimeAssetsProgress downloads any missing model files and archives
// needed for this run, reporting fine-grained per-asset progress (phase, byte
// counts, and a clamped percentage). The required asset set is resolved once
// from the asset manifest (ManifestFor), so URLs, file names, and extraction
// targets have a single source of truth; this function only performs the
// downloads. ctx cancels in-flight downloads (Ctrl-C during startup or render
// asset setup).
func EnsureRuntimeAssetsProgress(ctx context.Context, cfg *Config, req AssetRequest, onProgress func(AssetProgress)) error {
	manifest, err := ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	dir := ModelsDirFrom(cfg)
	if err := ensureManifestProgress(ctx, manifest, dir, onProgress); err != nil {
		return err
	}
	if req.NeedTTS && cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) {
		if qwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
			return ensureQwenTTSAssetsProgress(ctx, cfg, onProgress)
		}
		return nil
	}
	if req.NeedTTS && ManagedTTS(cfg) {
		if err := sanitizeKokoroLexicons(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not sanitize kokoro lexicons: %v\n", err)
		}
		// Best-effort: install thewh1teagle Kokoro v1.0 English weights
		// (same ONNX as Python samantha-cli) into models_dir/kokoro-v1.0-en.
		// Needs python3+numpy+onnx; multi-lang pack remains the fallback. This
		// seam only ever reports a percentage (no byte counts), so Bytes/Total
		// stay 0 and Pct is clamped as usual.
		TryEnsureKokoroV1EnglishPack(ctx, func(name string, pct float64) {
			if onProgress != nil {
				onProgress(AssetProgress{Asset: name, Phase: assetPhaseDownload, Pct: clampPct(pct)})
			}
		})
	}
	return nil
}

// EnsureRuntimeAssets is the coarse (name, pct) form kept for the existing
// callers (speaker enroll, onboarding install, render/narrate paths): a thin
// adapter over EnsureRuntimeAssetsProgress so they see the same values as
// before without taking on the richer AssetProgress shape.
func EnsureRuntimeAssets(ctx context.Context, cfg *Config, req AssetRequest, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssetsProgress(ctx, cfg, req, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// ensureManifest is the legacy (name, pct) core, kept as a thin adapter over
// ensureManifestProgress for its direct callers/tests.
func ensureManifest(ctx context.Context, manifest AssetManifest, dir string, onProgress func(name string, pct float64)) error {
	return ensureManifestProgress(ctx, manifest, dir, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// ensureManifestProgress downloads every missing file and archive in the
// manifest into dir, emitting one AssetProgress tick per phase transition.
// It is the parameterized core of EnsureRuntimeAssetsProgress, so it can be
// tested against a temp dir and a fake HTTP server. Already-present files and
// already-extracted archives are skipped (no re-download) but still emit a
// single "skipped" tick, so a no-op run still reports what it checked.
func ensureManifestProgress(ctx context.Context, manifest AssetManifest, dir string, onProgress func(AssetProgress)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	emit := func(asset, phase string, bytes, total int64) {
		if onProgress != nil {
			onProgress(newAssetProgress(asset, phase, bytes, total))
		}
	}

	// Individual file downloads.
	for _, m := range manifest.ModelFiles() {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, m.Name)
		if fileVerified(path, m.Size, m.SHA256) {
			emit(m.Name, assetPhaseSkipped, 0, 0)
			continue
		}

		emit(m.Name, assetPhaseStart, 0, 0)

		var lastBytes, lastTotal int64
		if err := downloadFile(ctx, path, m.URL, m.Name, m.Size, m.SHA256, func(bytes, total int64) {
			lastBytes, lastTotal = bytes, total
			emit(m.Name, assetPhaseDownload, bytes, total)
		}); err != nil {
			return err
		}
		// downloadFile only returns once copyBodyVerified's checksum/size
		// check has already passed, so verify is reported after the fact.
		emit(m.Name, assetPhaseVerify, lastBytes, lastTotal)
		emit(m.Name, assetPhaseDone, lastBytes, lastTotal)
	}

	// Archive downloads with extraction.
	for _, a := range manifest.ModelArchives(dir) {
		if err := ctx.Err(); err != nil {
			return err
		}
		targetDir := dir
		if a.TargetDir != "" {
			targetDir = a.TargetDir
		}

		installed, err := ensureArchiveInstalled(targetDir, a.ID, a.URL, a.SHA256, a.CheckFiles)
		if err != nil {
			return err
		}
		if installed {
			emit(a.Name, assetPhaseSkipped, 0, 0)
			continue
		}

		emit(a.Name, assetPhaseStart, 0, 0)

		var lastBytes, lastTotal int64
		if err := downloadAndExtractArchive(ctx, targetDir, a.ID, a.URL, a.Name, a.CheckFiles, a.SHA256, func(bytes, total int64) {
			lastBytes, lastTotal = bytes, total
			emit(a.Name, assetPhaseDownload, bytes, total)
		}); err != nil {
			return err
		}
		// downloadAndExtractArchive verifies the checksum, extracts, and
		// promotes the result before returning, so verify/extract/install
		// are reported in order after the fact, same as verify above.
		emit(a.Name, assetPhaseVerify, lastBytes, lastTotal)
		emit(a.Name, assetPhaseExtract, lastBytes, lastTotal)
		emit(a.Name, assetPhaseInstall, lastBytes, lastTotal)
		emit(a.Name, assetPhaseDone, lastBytes, lastTotal)
	}

	return nil
}

// EnsureModels preserves the old behavior for callers that still need the default asset set.
func EnsureModels(ctx context.Context, cfg *Config, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssets(ctx, cfg, DefaultAssetRequest(cfg), onProgress)
}

// EnsureQwenTTSTier installs the native Qwen3-TTS package so the given tier is
// ready, regardless of the active TTS provider — personas other than the
// active one may route speech through qwen. Empty tier uses the configured one.
func EnsureQwenTTSTier(ctx context.Context, cfg *Config, tier string, onProgress func(name string, pct float64)) error {
	return EnsureQwenTTSTierProgress(ctx, cfg, tier, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// EnsureQwenTTSTierProgress is EnsureQwenTTSTier with per-phase AssetProgress
// reporting, for callers (models ensure --json) that need it.
func EnsureQwenTTSTierProgress(ctx context.Context, cfg *Config, tier string, onProgress func(AssetProgress)) error {
	if cfg == nil {
		return nil
	}
	cfgCopy := *cfg
	if strings.TrimSpace(tier) != "" {
		cfgCopy.QwenTTSModelTier = tier
	}
	return ensureQwenTTSAssetsProgress(ctx, &cfgCopy, onProgress)
}

// qwenNativeAssetName is the stable AssetProgress.Asset identifier for every
// tick emitted while installing the native Qwen3-TTS package — the dynamic
// part of the qwen installer's own progress reporting (its "stage" string)
// carries the Phase field instead, so a client can group every tick from one
// install under a single asset key.
const qwenNativeAssetName = "Qwen3-TTS native package"

// ensureQwenTTSAssetsProgress installs the native multi-tier package only (no
// Python/uv), reporting AssetProgress ticks.
func ensureQwenTTSAssetsProgress(ctx context.Context, cfg *Config, onProgress func(AssetProgress)) error {
	modelsDir := ModelsDirFrom(cfg)
	tier := ""
	url := ""
	sha := ""
	if cfg != nil {
		tier = cfg.QwenTTSModelTier
		url, sha = qwen.ResolveNativeDownload(cfg.QwenTTSNativeURL, cfg.QwenTTSNativeSHA256)
	}

	if qwen.InspectNative(modelsDir, tier).Installed {
		if onProgress != nil {
			onProgress(newAssetProgress(qwenNativeAssetName, assetPhaseSkipped, 0, 0))
		}
		return nil
	}

	if onProgress != nil {
		onProgress(newAssetProgress(qwenNativeAssetName, assetPhaseStart, 0, 0))
	}

	_, err := qwen.EnsureNative(ctx, modelsDir, qwen.NativeEnsureOptions{
		URL: url, SHA256: sha, Tier: tier,
	}, func(stage string, pct float64) {
		if onProgress != nil {
			onProgress(AssetProgress{Asset: qwenNativeAssetName, Phase: stage, Pct: clampPct(pct)})
		}
	})
	return err
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

func ensureArchiveInstalled(dir, id, url, sha256Hex string, checkFiles []string) (bool, error) {
	if !archiveExtracted(dir, checkFiles) {
		return false, nil
	}
	if sha256Hex == "" {
		return true, nil
	}
	if archiveInstallMarkerValid(dir, id, url, sha256Hex, checkFiles) {
		return true, nil
	}
	if archiveInstallMarkerExists(dir, id) {
		return false, nil
	}
	if err := writeArchiveInstallMarker(dir, id, url, sha256Hex, checkFiles); err != nil {
		return false, fmt.Errorf("adopt extracted archive %s: %w", id, err)
	}
	return true, nil
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
func downloadAndExtractArchive(ctx context.Context, dir, id, url, name string, checkFiles []string, sha256Hex string, onProgress func(bytes, total int64)) error {
	resp, err := openDownload(ctx, url, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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

	if _, err := copyBodyVerified(tmp, resp.Body, resp.ContentLength, name, sha256Hex, onProgress); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}

	names, err := extractTarStream(bzip2.NewReader(tmp), dir, name, checkFiles)
	if err != nil {
		return err
	}
	if sha256Hex != "" {
		if err := writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex, checkFiles, names); err != nil {
			return fmt.Errorf("extract %s: write install marker: %w", name, err)
		}
	}
	return nil
}

// extractTarStream extracts a decompressed tar stream into dir. It extracts into
// a temp directory inside dir, verifies that every check file is present, then
// promotes each top-level entry into dir, so dir is never left with a partial or
// rejected extraction. It returns the promoted top-level entry names (relative to
// dir) so callers can record the archive's footprint. It is the testable core
// (no bzip2/HTTP).
func extractTarStream(r io.Reader, dir, name string, checkFiles []string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	tmpDir, err := os.MkdirTemp(dir, ".extract-*")
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpDir)
		}
	}()

	if err := extractTar(tar.NewReader(r), tmpDir, name); err != nil {
		return nil, err
	}
	if !archiveExtracted(tmpDir, checkFiles) {
		return nil, fmt.Errorf("extract %s: archive missing expected files after extraction", name)
	}

	// Promote each top-level entry into the final directory, replacing any stale
	// partial from a previous run.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", name, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		src := filepath.Join(tmpDir, e.Name())
		dst := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(dst); err != nil {
			return nil, fmt.Errorf("extract %s: clear %s: %w", name, e.Name(), err)
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("extract %s: promote %s: %w", name, e.Name(), err)
		}
	}

	committed = true
	os.RemoveAll(tmpDir)
	return names, nil
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

func fileVerified(path string, expectedSize int64, sha256Hex string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return false
	}
	if sha256Hex != "" {
		got, err := fileSHA256(path)
		if err != nil || !strings.EqualFold(got, sha256Hex) {
			return false
		}
	}
	return true
}

type archiveInstallMarker struct {
	ID          string            `json:"id"`
	URL         string            `json:"url"`
	SHA256      string            `json:"sha256"`
	CheckHashes map[string]string `json:"check_hashes"`
	// Files are the top-level entry names promoted into the target dir at
	// extraction, relative to it. It records the archive's full on-disk footprint
	// (not just the check-file subset) so cleanup knows every path the asset owns.
	// Omitted by markers written before this field existed; readers must treat an
	// absent list conservatively.
	Files []string `json:"files,omitempty"`
}

func archiveInstallMarkerPath(dir, id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "archive"
	}
	return filepath.Join(dir, ".samantha-asset-"+name+".json")
}

func archiveInstallMarkerExists(dir, id string) bool {
	_, err := os.Stat(archiveInstallMarkerPath(dir, id))
	return err == nil
}

// archiveMarkerFiles returns the recorded top-level extraction footprint for an
// archive and whether a parseable marker exists. files is nil for legacy markers
// written before the footprint was recorded, so callers must distinguish "marker
// present but footprint unknown" (present=true, files=nil) from "no marker".
func archiveMarkerFiles(dir, id string) (files []string, present bool) {
	data, err := os.ReadFile(archiveInstallMarkerPath(dir, id))
	if err != nil {
		return nil, false
	}
	var marker archiveInstallMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, false
	}
	return marker.Files, true
}

func archiveInstallMarkerValid(dir, id, url, sha256Hex string, checkFiles []string) bool {
	data, err := os.ReadFile(archiveInstallMarkerPath(dir, id))
	if err != nil {
		return false
	}
	var marker archiveInstallMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	if marker.ID != id || marker.URL != url || !strings.EqualFold(marker.SHA256, sha256Hex) {
		return false
	}
	wantHashes, err := archiveCheckHashes(dir, checkFiles)
	if err != nil {
		return false
	}
	if len(marker.CheckHashes) != len(wantHashes) {
		return false
	}
	for path, got := range wantHashes {
		if !strings.EqualFold(got, marker.CheckHashes[path]) {
			return false
		}
	}
	return true
}

func writeArchiveInstallMarker(dir, id, url, sha256Hex string, checkFiles []string) error {
	return writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex, checkFiles, nil)
}

// writeArchiveInstallMarkerFiles is writeArchiveInstallMarker with the extracted
// top-level entry list recorded (see archiveInstallMarker.Files). files is nil
// when the footprint is unknown (e.g. adopting an already-extracted archive),
// which readers treat as a legacy marker.
func writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex string, checkFiles, files []string) error {
	hashes, err := archiveCheckHashes(dir, checkFiles)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(archiveInstallMarker{
		ID:          id,
		URL:         url,
		SHA256:      strings.ToLower(sha256Hex),
		CheckHashes: hashes,
		Files:       files,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := archiveInstallMarkerPath(dir, id)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
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
	if _, err := tmp.Write(data); err != nil {
		return err
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

func archiveCheckHashes(dir string, checkFiles []string) (map[string]string, error) {
	hashes := make(map[string]string, len(checkFiles))
	for _, rel := range checkFiles {
		sum, err := pathSHA256(filepath.Join(dir, rel))
		if err != nil {
			return nil, err
		}
		hashes[rel] = sum
	}
	return hashes, nil
}

func pathSHA256(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fileSHA256(path)
	}
	hasher := sha256.New()
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hasher.Write([]byte(rel))
		hasher.Write([]byte{0})
		if d.IsDir() {
			hasher.Write([]byte{1})
			return nil
		}
		hasher.Write([]byte{2})
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(hasher, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// copyBodyVerified streams body into dst (hashing when sha256Hex is set),
// reporting the raw bytes-written/total against total (percentage is derived
// once, by the caller, so it can be clamped in one place), and verifies the
// checksum after the copy. It returns the byte count written. name labels the
// asset in error messages. It is the single copy/verify core shared by file
// and archive downloads.
func copyBodyVerified(dst io.Writer, body io.Reader, total int64, name, sha256Hex string, onProgress func(bytes, total int64)) (int64, error) {
	hasher := sha256.New()
	sink := dst
	if sha256Hex != "" {
		sink = io.MultiWriter(dst, hasher)
	}

	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return written, fmt.Errorf("download %s: %w", name, werr)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, fmt.Errorf("download %s: %w", name, readErr)
		}
	}

	if sha256Hex != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, sha256Hex) {
			return written, fmt.Errorf("download %s: checksum mismatch, got %s want %s (if upstream republished this file, update samantha or install the model manually into the models dir)", name, got, sha256Hex)
		}
	}
	return written, nil
}

// downloadFile downloads url into path. It writes to a temp file in the target
// directory, verifies the expected size and checksum when known, and atomically
// renames into place on success. The temp file is removed on any failure, so a
// partial or corrupt download never lands at path. file names the asset in
// error messages.
func downloadFile(ctx context.Context, path, url, file string, size int64, sha256Hex string, onProgress func(bytes, total int64)) error {
	resp, err := openDownload(ctx, url, file)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create parent dir: %w", file, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.part")
	if err != nil {
		return fmt.Errorf("download %s: create temp file: %w", file, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	written, err := copyBodyVerified(tmp, resp.Body, resp.ContentLength, file, sha256Hex, onProgress)
	if err != nil {
		return err
	}
	if size > 0 && written != size {
		return fmt.Errorf("download %s: size mismatch, got %d bytes want %d", file, written, size)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("download %s: close temp file: %w", file, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("download %s: finalize: %w", file, err)
	}
	committed = true
	return nil
}
