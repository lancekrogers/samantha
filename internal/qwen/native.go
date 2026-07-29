package qwen

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Native product install lives under models_dir/qwen3-tts/ (release tarball layout).
const (
	nativeInstallSchema = "qwen3-tts-native.install.v1"
	NativeRootName      = "qwen3-tts"
	DefaultModelTier    = "0.6b"
	Tier1_7B            = "1.7b"
)

// NativePaths is the ensure install tree (see lab docs/DISTRIBUTION.md).
type NativePaths struct {
	Root        string
	BinDir      string
	Worker      string
	CLI         string
	ModelDir    string
	InstallJSON string
	SHA256SUMS  string
	PresetsJSON string
	CacheDir    string
}

// NativeInstallPaths returns paths under modelsDir/qwen3-tts.
func NativeInstallPaths(modelsDir string) NativePaths {
	root := filepath.Join(modelsDir, NativeRootName)
	bin := filepath.Join(root, "bin")
	models := filepath.Join(root, "models")
	worker := filepath.Join(bin, "qwen3-tts-worker")
	if runtime.GOOS == "windows" {
		worker += ".exe"
	}
	cli := filepath.Join(bin, "qwen3-tts-cli")
	if runtime.GOOS == "windows" {
		cli += ".exe"
	}
	return NativePaths{
		Root:        root,
		BinDir:      bin,
		Worker:      worker,
		CLI:         cli,
		ModelDir:    models,
		InstallJSON: filepath.Join(root, "install.json"),
		SHA256SUMS:  filepath.Join(root, "SHA256SUMS"),
		PresetsJSON: filepath.Join(models, "presets", "presets.json"),
		CacheDir:    filepath.Join(root, "cache", "speaker-embeddings"),
	}
}

// NativeStatus is the inspect/ensure result for the native package.
type NativeStatus struct {
	Installed    bool     `json:"installed"`
	WorkerReady  bool     `json:"worker_ready"`
	ModelReady   bool     `json:"model_ready"`
	PresetsReady bool     `json:"presets_ready"`
	DefaultTier  string   `json:"default_tier"`
	TiersReady   []string `json:"tiers_ready"`
	TiersMissing []string `json:"tiers_missing,omitempty"`
	EngineSHA    string   `json:"engine_sha,omitempty"`
	RepoCommit   string   `json:"repo_commit,omitempty"`
	Root         string   `json:"root"`
	Worker       string   `json:"worker"`
	ModelDir     string   `json:"model_dir"`
	Detail       string   `json:"detail,omitempty"`
}

// nativeInstallJSON is a subset of lab install.json (qwen3-tts-native.install.v1).
type nativeInstallJSON struct {
	Schema      string `json:"schema"`
	RepoCommit  string `json:"repo_commit"`
	EngineSHA   string `json:"engine_sha"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	TierDefault string `json:"tier_default"`
	SampleRate  int    `json:"sample_rate"`
	Protocol    string `json:"protocol"`
	Streaming   bool   `json:"streaming"`
	Bin         struct {
		Worker       string `json:"worker"`
		WorkerSHA256 string `json:"worker_sha256"`
		CLI          string `json:"cli"`
		CLISHA256    string `json:"cli_sha256"`
		Lib          string `json:"lib"`
		LibSHA256    string `json:"lib_sha256"`
	} `json:"bin"`
	Models map[string]struct {
		Quant     string  `json:"quant"`
		TTS       fileRef `json:"tts"`
		Tokenizer fileRef `json:"tokenizer"`
	} `json:"models"`
	Presets       string `json:"presets"`
	PresetsSHA256 string `json:"presets_sha256"`
}

type fileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// InspectNative reports whether a product native package is installed and which tiers are ready.
func InspectNative(modelsDir, preferredTier string) NativeStatus {
	p := NativeInstallPaths(modelsDir)
	st := NativeStatus{
		Root: p.Root, Worker: p.Worker, ModelDir: p.ModelDir,
		DefaultTier: DefaultModelTier,
	}
	if preferredTier = normalizeTier(preferredTier); preferredTier != "" {
		st.DefaultTier = preferredTier
	}

	st.WorkerReady = regularFile(p.Worker) || isExecutable(p.Worker)
	st.PresetsReady = regularFile(p.PresetsJSON)

	var install nativeInstallJSON
	if data, err := os.ReadFile(p.InstallJSON); err == nil {
		_ = json.Unmarshal(data, &install)
		if install.TierDefault != "" {
			st.DefaultTier = normalizeTier(install.TierDefault)
			if preferredTier != "" {
				st.DefaultTier = preferredTier
			}
		}
		st.EngineSHA = install.EngineSHA
		st.RepoCommit = install.RepoCommit
	}

	// Discover tiers from install.json and/or on-disk GGUF names.
	known := []string{DefaultModelTier, Tier1_7B}
	if len(install.Models) > 0 {
		known = known[:0]
		for tier := range install.Models {
			known = append(known, normalizeTier(tier))
		}
		if len(known) == 0 {
			known = []string{DefaultModelTier, Tier1_7B}
		}
	}
	for _, tier := range known {
		if tierReady(p, install, tier) {
			st.TiersReady = append(st.TiersReady, tier)
		} else if _, listed := install.Models[tier]; listed || tier == DefaultModelTier || tier == preferredTier {
			st.TiersMissing = append(st.TiersMissing, tier)
		}
	}

	// Preferred tier must be ready for ModelReady.
	want := st.DefaultTier
	st.ModelReady = tierReady(p, install, want)
	st.Installed = st.WorkerReady && st.ModelReady && st.PresetsReady
	switch {
	case st.Installed:
		st.Detail = fmt.Sprintf("native Qwen3-TTS installed (tier %s)", want)
	case !st.WorkerReady && !st.ModelReady:
		st.Detail = "native Qwen3-TTS package is not installed"
	default:
		st.Detail = "native Qwen3-TTS installation is incomplete"
	}
	return st
}

func normalizeTier(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.ReplaceAll(t, "B", "b")
	switch t {
	case "0.6", "0.6b", "600m":
		return DefaultModelTier
	case "1.7", "1.7b":
		return Tier1_7B
	default:
		return t
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0o111 != 0
}

func tierReady(p NativePaths, install nativeInstallJSON, tier string) bool {
	tier = normalizeTier(tier)
	if m, ok := install.Models[tier]; ok {
		ttsPath := filepath.Join(p.Root, filepath.FromSlash(m.TTS.Path))
		tokPath := filepath.Join(p.Root, filepath.FromSlash(m.Tokenizer.Path))
		return regularFile(ttsPath) && regularFile(tokPath)
	}
	// Flat lab layout fallback: models/qwen3-tts-0.6b-f16.gguf
	switch tier {
	case DefaultModelTier:
		return regularFile(filepath.Join(p.ModelDir, "qwen3-tts-0.6b-f16.gguf")) &&
			regularFile(filepath.Join(p.ModelDir, "qwen3-tts-tokenizer-f16.gguf"))
	case Tier1_7B:
		// Common names once convert lands; any match counts as ready.
		patterns := []string{
			"qwen3-tts-1.7b-f16.gguf",
			"qwen3-tts-1.7b-q8_0.gguf",
			"qwen3-tts-1.7b-*.gguf",
		}
		for _, pat := range patterns {
			matches, _ := filepath.Glob(filepath.Join(p.ModelDir, pat))
			if len(matches) > 0 && regularFile(filepath.Join(p.ModelDir, "qwen3-tts-tokenizer-f16.gguf")) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// NativeEnsureOptions configures tarball install for multi-tier native assets.
type NativeEnsureOptions struct {
	// URL is http(s) or file:// path to qwen3-tts-native-*.tar.gz.
	URL string
	// SHA256 of the compressed archive (optional but recommended).
	SHA256 string
	// Tier is the required model tier (default 0.6b). 1.7b fails closed if missing.
	Tier string
	// Force re-downloads even when InspectNative reports installed.
	Force bool
}

// EnsureNative installs or verifies the native release package under modelsDir/qwen3-tts.
// It never installs Python. When URL is empty and the package is already present, it
// only verifies the requested tier. When URL is empty and missing, it returns a
// clear remediation error (set qwen_tts_native_url / SAMANTHA_QWEN_NATIVE_URL).
func EnsureNative(ctx context.Context, modelsDir string, opt NativeEnsureOptions, progress ProgressFunc) (NativeStatus, error) {
	if strings.TrimSpace(modelsDir) == "" {
		return NativeStatus{}, errors.New("native Qwen setup: models directory is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tier := normalizeTier(opt.Tier)
	if tier == "" {
		tier = DefaultModelTier
	}

	status := InspectNative(modelsDir, tier)
	// Package present with required tier → done (multi-tier optional files may still be missing).
	if status.WorkerReady && status.PresetsReady && status.ModelReady && !opt.Force {
		if progress != nil {
			progress("native Qwen3-TTS", 100)
		}
		return status, nil
	}
	// Package present but requested tier absent (e.g. 1.7B not shipped / engine-blocked).
	if status.WorkerReady && status.PresetsReady && !status.ModelReady {
		if tier == Tier1_7B {
			return status, fmt.Errorf("native Qwen3-TTS tier %s is not in this package (engine/convert may still block 1.7B); keep tier 0.6b or install a multi-tier release", tier)
		}
		// Fall through to re-download if URL provided; otherwise incomplete install error.
	}

	url := strings.TrimSpace(opt.URL)
	if url == "" {
		url = strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_NATIVE_URL"))
	}
	if url == "" {
		if status.Installed {
			return status, nil
		}
		return status, fmt.Errorf("native Qwen3-TTS is not installed; set qwen_tts_native_url (or SAMANTHA_QWEN_NATIVE_URL) to a release tarball, then run models ensure --tts")
	}

	if progress != nil {
		progress("native Qwen3-TTS package", 5)
	}
	p := NativeInstallPaths(modelsDir)
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		return NativeStatus{}, fmt.Errorf("native Qwen setup: create %s: %w", p.Root, err)
	}

	ctx, cancel := context.WithTimeout(ctx, installerTimeout)
	defer cancel()

	tmpArchive, err := downloadNativeArchive(ctx, p.Root, url, opt.SHA256, progress)
	if err != nil {
		return NativeStatus{}, err
	}
	defer os.Remove(tmpArchive)

	if progress != nil {
		progress("native Qwen3-TTS extract", 70)
	}
	if err := extractNativeTarGz(tmpArchive, p.Root); err != nil {
		return NativeStatus{}, err
	}
	// Ensure cache dir exists for clone embeddings (product runtime).
	_ = os.MkdirAll(p.CacheDir, 0o755)
	// Ensure worker is executable after extract.
	if regularFile(p.Worker) {
		_ = os.Chmod(p.Worker, 0o755)
	}
	if regularFile(p.CLI) {
		_ = os.Chmod(p.CLI, 0o755)
	}
	// Recreate common dylib names if only the versioned file was shipped.
	fixDylibNames(p.BinDir)

	if progress != nil {
		progress("native Qwen3-TTS verify", 90)
	}
	status = InspectNative(modelsDir, tier)
	if !status.Installed {
		return status, fmt.Errorf("native Qwen setup incomplete: %s", status.Detail)
	}
	if !status.ModelReady {
		if tier == Tier1_7B {
			return status, fmt.Errorf("native package installed but tier %s is not present (1.7B may be engine-blocked); use tier 0.6b", tier)
		}
		return status, fmt.Errorf("native package installed but tier %s models are missing", tier)
	}
	if progress != nil {
		progress("native Qwen3-TTS", 100)
	}
	return status, nil
}

func downloadNativeArchive(ctx context.Context, root, url, wantSHA string, progress ProgressFunc) (string, error) {
	tmp, err := os.CreateTemp(root, ".qwen-native-*.tar.gz.part")
	if err != nil {
		return "", fmt.Errorf("native Qwen setup: temp archive: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	var body io.ReadCloser
	var contentLen int64
	switch {
	case strings.HasPrefix(url, "file://"):
		path := strings.TrimPrefix(url, "file://")
		if runtime.GOOS == "windows" && strings.HasPrefix(path, "/") && len(path) > 2 && path[2] == ':' {
			path = path[1:]
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("native Qwen setup: open %s: %w", path, err)
		}
		body = f
		if st, err := f.Stat(); err == nil {
			contentLen = st.Size()
		}
	case strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("native Qwen setup: download: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("native Qwen setup: download HTTP %d for %s", resp.StatusCode, url)
		}
		body = resp.Body
		contentLen = resp.ContentLength
	default:
		// Bare filesystem path.
		f, err := os.Open(url)
		if err != nil {
			return "", fmt.Errorf("native Qwen setup: open %s: %w", url, err)
		}
		body = f
		if st, err := f.Stat(); err == nil {
			contentLen = st.Size()
		}
	}
	defer body.Close()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	var written int64
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return "", err
			}
			written += int64(n)
			if progress != nil && contentLen > 0 {
				pct := 5 + 60*float64(written)/float64(contentLen)
				if pct > 65 {
					pct = 65
				}
				progress("native Qwen3-TTS package", pct)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("native Qwen setup: download: %w", readErr)
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(wantSHA, sum) {
		return "", fmt.Errorf("native Qwen setup: archive sha256 mismatch (got %s want %s)", sum, wantSHA)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ok = true
	return tmpName, nil
}

func extractNativeTarGz(archivePath, destRoot string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("native Qwen setup: gzip: %w", err)
	}
	defer gz.Close()

	// Extract into a staging dir, then promote stripped layout into destRoot.
	stage, err := os.MkdirTemp(filepath.Dir(destRoot), ".qwen-native-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	tr := tar.NewReader(gz)
	var prefix string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("native Qwen setup: tar: %w", err)
		}
		if strings.HasPrefix(hdr.Name, "/") || filepath.IsAbs(hdr.Name) {
			return fmt.Errorf("native Qwen setup: refusing absolute path %q", hdr.Name)
		}
		if prefix == "" {
			if parts := strings.SplitN(hdr.Name, "/", 2); len(parts) > 1 {
				prefix = parts[0] + "/"
			}
		}
		rel := strings.TrimPrefix(hdr.Name, prefix)
		if rel == "" || rel == "." {
			continue
		}
		target, err := safeJoinNative(stage, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)|0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// Allow relative symlinks that stay inside the package (dylib names).
			if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(hdr.Linkname, "/") {
				return fmt.Errorf("native Qwen setup: refusing absolute symlink %q", hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				// If symlink fails (e.g. Windows without privilege), skip — fixDylibNames later.
				continue
			}
		default:
			// ignore other types
		}
	}

	// Promote staged install.json / bin / models into destRoot (merge/replace).
	for _, name := range []string{"install.json", "SHA256SUMS", "bin", "models"} {
		src := filepath.Join(stage, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(destRoot, name)
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("native Qwen setup: clear %s: %w", name, err)
		}
		if err := os.Rename(src, dst); err != nil {
			// Cross-device: copy.
			if err := copyPath(src, dst); err != nil {
				return fmt.Errorf("native Qwen setup: promote %s: %w", name, err)
			}
		}
	}
	return nil
}

func safeJoinNative(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe absolute path %q", rel)
	}
	target := filepath.Join(dir, rel)
	within, err := filepath.Rel(dir, target)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes archive root", rel)
	}
	return target, nil
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fixDylibNames(binDir string) {
	// Prefer real versioned file → create .0 and unversioned names when missing.
	candidates := []string{
		"libqwen3tts.0.1.0.dylib",
		"libqwen3tts.0.dylib",
		"libqwen3tts.dylib",
		"libqwen3tts.so.0.1.0",
		"libqwen3tts.so.0",
		"libqwen3tts.so",
	}
	var real string
	for _, c := range candidates {
		path := filepath.Join(binDir, c)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			real = c
			break
		}
	}
	if real == "" {
		return
	}
	// Best-effort names used by @rpath / loader.
	if strings.HasSuffix(real, ".dylib") {
		for _, name := range []string{"libqwen3tts.0.dylib", "libqwen3tts.dylib"} {
			dst := filepath.Join(binDir, name)
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			_ = os.Symlink(real, dst)
		}
	}
}

// ResolveNativeURL returns configured URL or env override (tests/operators).
func ResolveNativeURL(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_NATIVE_URL"))
}

// ResolveNativeSHA256 returns configured archive checksum or env override.
func ResolveNativeSHA256(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_NATIVE_SHA256"))
}

// PreferNative reports whether product path should use native package when
// binary/model are empty (managed selection). Always true once native is the
// intended product; still allows explicit external CLI/worker paths.
func PreferNative() bool {
	// Feature flag for gradual rollout; default on so ensure installs native first.
	v := strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_PREFER_NATIVE"))
	if v == "" {
		return true
	}
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// NativeInstalled is a shorthand for InspectNative(...).Installed with default tier.
func NativeInstalled(modelsDir string) bool {
	return InspectNative(modelsDir, DefaultModelTier).Installed
}
