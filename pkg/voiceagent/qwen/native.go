package qwen

import (
	"archive/tar"
	"bytes"
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
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Native product install lives under models_dir/qwen3-tts/ (release tarball layout).
const (
	nativeInstallSchema = "qwen3-tts-native.install.v1"
	NativeRootName      = "qwen3-tts"
	DefaultModelTier    = "0.6b"
	Tier1_7B            = "1.7b"
	// installerTimeout bounds download + extract for EnsureNative.
	installerTimeout = 30 * time.Minute
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
	Installed    bool `json:"installed"`
	WorkerReady  bool `json:"worker_ready"`
	ModelReady   bool `json:"model_ready"`
	PresetsReady bool `json:"presets_ready"`
	// RuntimeReady is true when bin/ ships the shared libs the worker needs
	// (libqwen3tts + libggml*). Older Darwin packages only shipped libqwen3tts
	// and relied on absolute lab RPATHs — those report RuntimeReady=false so
	// ensure/doctor force a portable reinstall instead of mid-session dyld falls.
	RuntimeReady bool     `json:"runtime_ready"`
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
	Product     string `json:"product"`
	RepoCommit  string `json:"repo_commit"`
	EngineSHA   string `json:"engine_sha"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	BackendHint string `json:"backend_hint"`
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
	UserInstall   string `json:"user_install"`
}

type fileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// nativeFileVerify selects how strictly manifest files are checked.
//
// Presence is for InspectNative / status chips / TUI: path safety + exists +
// executable bit only. Full SHA-256 of multi-GB GGUFs must never run on the
// Bubble Tea render path — that was re-hashing ~2 GiB on every keystroke.
// Hash is for EnsureNative after extract (and explicit integrity checks).
type nativeFileVerify int

const (
	nativeFilePresent nativeFileVerify = iota
	nativeFileHash
)

// InspectNative reports whether a product native package is installed and which tiers are ready.
//
// It deliberately does not re-hash model weights. Call VerifyNativeInstall when
// a full integrity check is required (post-install, doctor, Force ensure).
func InspectNative(modelsDir, preferredTier string) NativeStatus {
	p := NativeInstallPaths(modelsDir)
	st := NativeStatus{
		Root: p.Root, Worker: p.Worker, ModelDir: p.ModelDir,
		DefaultTier: DefaultModelTier,
	}
	if preferredTier = normalizeTier(preferredTier); preferredTier != "" {
		st.DefaultTier = preferredTier
	}

	install, err := loadAndVerifyNativeInstall(p, nativeFilePresent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			st.Detail = "native Qwen3-TTS package is not installed"
		} else {
			st.Detail = "native Qwen3-TTS install manifest is invalid: " + err.Error()
		}
		return st
	}

	st.WorkerReady = isExecutable(p.Worker)
	st.PresetsReady = true // loadAndVerifyNativeInstall confirmed the presets path exists.
	st.RuntimeReady = runtimeLibsReady(p.BinDir)
	if install.TierDefault != "" {
		st.DefaultTier = normalizeTier(install.TierDefault)
		if preferredTier != "" {
			st.DefaultTier = preferredTier
		}
	}
	st.EngineSHA = install.EngineSHA
	st.RepoCommit = install.RepoCommit

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
		sort.Strings(known)
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
	st.Installed = st.WorkerReady && st.ModelReady && st.PresetsReady && st.RuntimeReady
	switch {
	case st.Installed:
		st.Detail = fmt.Sprintf("native Qwen3-TTS installed (tier %s)", want)
	case st.WorkerReady && st.ModelReady && st.PresetsReady && !st.RuntimeReady:
		st.Detail = "native Qwen3-TTS package is missing runtime libraries (libqwen3tts/libggml); re-run models ensure --tts for a portable package"
	case !st.WorkerReady && !st.ModelReady:
		st.Detail = "native Qwen3-TTS package is not installed"
	default:
		st.Detail = "native Qwen3-TTS installation is incomplete"
	}
	return st
}

// runtimeLibsReady reports whether bin/ contains the shared libraries a product
// worker needs at load time. Presence-only (no dyld spawn) so TUI inspect stays
// cheap; package_release must ship these next to the worker.
func runtimeLibsReady(binDir string) bool {
	if strings.TrimSpace(binDir) == "" {
		return false
	}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return false
	}
	hasQwenLib := false
	hasGgml := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip AppleDouble noise from some extractors.
		if strings.HasPrefix(name, "._") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "libqwen3tts") && isSharedLibName(lower) {
			hasQwenLib = true
		}
		if strings.HasPrefix(lower, "libggml") && isSharedLibName(lower) {
			hasGgml = true
		}
	}
	return hasQwenLib && hasGgml
}

func isSharedLibName(lower string) bool {
	return strings.Contains(lower, ".dylib") || strings.Contains(lower, ".so")
}

// VerifyNativeInstall re-checks the on-disk package against install.json
// SHA-256 digests. This is intentionally separate from InspectNative so status
// chips and TUI frames stay O(stat), not O(model size).
func VerifyNativeInstall(modelsDir string) error {
	p := NativeInstallPaths(modelsDir)
	_, err := loadAndVerifyNativeInstall(p, nativeFileHash)
	return err
}

func loadAndVerifyNativeInstall(p NativePaths, mode nativeFileVerify) (nativeInstallJSON, error) {
	data, err := os.ReadFile(p.InstallJSON)
	if err != nil {
		return nativeInstallJSON{}, err
	}
	var install nativeInstallJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&install); err != nil {
		return nativeInstallJSON{}, fmt.Errorf("decode install.json: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nativeInstallJSON{}, err
	}
	if install.Schema != nativeInstallSchema {
		return nativeInstallJSON{}, fmt.Errorf("schema %q, want %q", install.Schema, nativeInstallSchema)
	}
	if install.OS != runtime.GOOS || install.Arch != runtime.GOARCH {
		return nativeInstallJSON{}, fmt.Errorf("platform %s/%s, want %s/%s", install.OS, install.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if install.Protocol != "qwen3-tts-worker/v1" {
		return nativeInstallJSON{}, fmt.Errorf("protocol %q, want qwen3-tts-worker/v1", install.Protocol)
	}
	if install.SampleRate != 24000 {
		return nativeInstallJSON{}, fmt.Errorf("sample_rate %d, want 24000", install.SampleRate)
	}
	defaultTier := normalizeTier(install.TierDefault)
	if defaultTier == "" || defaultTier != install.TierDefault {
		return nativeInstallJSON{}, fmt.Errorf("invalid tier_default %q", install.TierDefault)
	}
	if len(install.Models) == 0 {
		return nativeInstallJSON{}, errors.New("models is empty")
	}
	if _, ok := install.Models[defaultTier]; !ok {
		return nativeInstallJSON{}, fmt.Errorf("tier_default %q is not listed in models", defaultTier)
	}

	workerRel, err := filepath.Rel(p.Root, p.Worker)
	if err != nil {
		return nativeInstallJSON{}, fmt.Errorf("resolve worker path: %w", err)
	}
	workerRel = filepath.ToSlash(workerRel)
	if install.Bin.Worker != workerRel {
		return nativeInstallJSON{}, fmt.Errorf("worker path %q, want %q", install.Bin.Worker, workerRel)
	}
	if err := verifyNativeManifestFile(p.Root, install.Bin.Worker, install.Bin.WorkerSHA256, true, mode); err != nil {
		return nativeInstallJSON{}, fmt.Errorf("worker: %w", err)
	}
	for _, ref := range []struct {
		name string
		path string
		sha  string
	}{
		{name: "cli", path: install.Bin.CLI, sha: install.Bin.CLISHA256},
		{name: "lib", path: install.Bin.Lib, sha: install.Bin.LibSHA256},
	} {
		if ref.path == "" && ref.sha == "" {
			continue
		}
		if ref.path == "" || ref.sha == "" {
			return nativeInstallJSON{}, fmt.Errorf("%s path and sha256 must both be set", ref.name)
		}
		if err := verifyNativeManifestFile(p.Root, ref.path, ref.sha, false, mode); err != nil {
			return nativeInstallJSON{}, fmt.Errorf("%s: %w", ref.name, err)
		}
	}

	presetsRel, err := filepath.Rel(p.Root, p.PresetsJSON)
	if err != nil {
		return nativeInstallJSON{}, fmt.Errorf("resolve presets path: %w", err)
	}
	presetsRel = filepath.ToSlash(presetsRel)
	if install.Presets != presetsRel {
		return nativeInstallJSON{}, fmt.Errorf("presets path %q, want %q", install.Presets, presetsRel)
	}
	if err := verifyNativeManifestFile(p.Root, install.Presets, install.PresetsSHA256, false, mode); err != nil {
		return nativeInstallJSON{}, fmt.Errorf("presets: %w", err)
	}

	for tier, model := range install.Models {
		if normalized := normalizeTier(tier); normalized == "" || normalized != tier {
			return nativeInstallJSON{}, fmt.Errorf("invalid model tier %q", tier)
		}
		if err := verifyNativeManifestFile(p.Root, model.TTS.Path, model.TTS.SHA256, false, mode); err != nil {
			return nativeInstallJSON{}, fmt.Errorf("model %s tts: %w", tier, err)
		}
		if err := verifyNativeManifestFile(p.Root, model.Tokenizer.Path, model.Tokenizer.SHA256, false, mode); err != nil {
			return nativeInstallJSON{}, fmt.Errorf("model %s tokenizer: %w", tier, err)
		}
	}
	return install, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("install.json contains multiple JSON values")
		}
		return fmt.Errorf("decode install.json trailing data: %w", err)
	}
	return nil
}

func verifyNativeManifestFile(root, manifestPath, wantSHA string, executable bool, mode nativeFileVerify) error {
	rel, err := cleanNativeManifestPath(manifestPath)
	if err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%q is not a regular file", manifestPath)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if err := requirePathWithinNative(root, resolved, manifestPath); err != nil {
		return err
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !resolvedInfo.Mode().IsRegular() {
		return fmt.Errorf("%q does not resolve to a regular file", manifestPath)
	}
	if executable && resolvedInfo.Mode()&0o111 == 0 {
		return fmt.Errorf("%q is not executable", manifestPath)
	}
	// Presence-only: enough for status chips. Full content hashing of multi-GB
	// weights is reserved for post-install integrity (nativeFileHash).
	if mode == nativeFilePresent {
		return nil
	}
	want, err := hex.DecodeString(strings.TrimSpace(wantSHA))
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("invalid sha256 %q", wantSHA)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := h.Sum(nil)
	if !bytes.Equal(got, want) {
		return fmt.Errorf("sha256 mismatch for %q (got %s want %s)", manifestPath, hex.EncodeToString(got), strings.ToLower(wantSHA))
	}
	return nil
}

func cleanNativeManifestPath(manifestPath string) (string, error) {
	nativePath := filepath.FromSlash(manifestPath)
	if manifestPath == "" || strings.Contains(manifestPath, `\`) || path.IsAbs(manifestPath) ||
		filepath.IsAbs(nativePath) || filepath.VolumeName(nativePath) != "" {
		return "", fmt.Errorf("unsafe manifest path %q", manifestPath)
	}
	clean := path.Clean(manifestPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != manifestPath {
		return "", fmt.Errorf("unsafe manifest path %q", manifestPath)
	}
	return clean, nil
}

func requirePathWithinNative(root, target, original string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	// Temp roots on macOS commonly spell /private/var as /var. Compare the
	// resolved forms only when both paths exist; otherwise keep both lexical.
	if resolvedRoot, rootErr := filepath.EvalSymlinks(rootAbs); rootErr == nil {
		if resolvedTarget, targetErr := filepath.EvalSymlinks(targetAbs); targetErr == nil {
			rootAbs = resolvedRoot
			targetAbs = resolvedTarget
		}
	}
	within, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe path %q escapes install root", original)
	}
	return nil
}

// NormalizeModelTier canonicalizes tier names for config and worker env
// (0.6B → 0.6b, 1.7 → 1.7b). Empty input returns DefaultModelTier.
func NormalizeModelTier(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return DefaultModelTier
	}
	switch t {
	case "0.6", "0.6b", "600m", "0b6":
		return DefaultModelTier
	case "1.7", "1.7b", "1b7":
		return Tier1_7B
	default:
		return t
	}
}

func normalizeTier(t string) string { return NormalizeModelTier(t) }

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0o111 != 0
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
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
// clear remediation error (no URL and no published default for this platform).
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
	// Package present with required tier + runtime libs → done (multi-tier optional files may still be missing).
	// RuntimeReady=false forces re-ensure so Darwin packages that only shipped
	// libqwen3tts (lab absolute RPATH) are replaced with portable releases.
	if status.WorkerReady && status.PresetsReady && status.ModelReady && status.RuntimeReady && !opt.Force {
		if progress != nil {
			progress("native Qwen3-TTS", 100)
		}
		return status, nil
	}
	// Package present but requested tier absent (e.g. 1.7B not shipped / engine-blocked).
	if status.WorkerReady && status.PresetsReady && !status.ModelReady {
		// Fail fast for 1.7B only when the download source is the pinned
		// default release, which is known to ship 0.6b alone — re-fetching it
		// cannot add the tier. A configured URL (qwen_tts_native_url or env)
		// is how a multi-tier tarball arrives, so it falls through to the
		// re-download below instead.
		if tier == Tier1_7B {
			defaultURL, _ := DefaultNativeRelease()
			effective := strings.TrimSpace(opt.URL)
			if effective == "" {
				effective = ResolveNativeURL("")
			}
			if effective == "" || effective == defaultURL {
				return status, fmt.Errorf("native Qwen3-TTS tier %s is not in the pinned release; set qwen_tts_native_url to a multi-tier tarball or keep tier 0.6b", tier)
			}
		}
		// Fall through to re-download if URL provided; otherwise incomplete install error.
	}

	url := strings.TrimSpace(opt.URL)
	if url == "" {
		url = ResolveNativeURL("")
	}
	if strings.TrimSpace(opt.SHA256) == "" {
		opt.SHA256 = ResolveNativeSHA256("")
	}
	if url == "" {
		if status.Installed {
			return status, nil
		}
		return status, fmt.Errorf("native Qwen3-TTS is not installed; no published package for %s/%s — set qwen_tts_native_url to a release tarball, then run models ensure --tts", runtime.GOOS, runtime.GOARCH)
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
	// Full digest check once after extract — not on every later InspectNative.
	if err := VerifyNativeInstall(modelsDir); err != nil {
		status = InspectNative(modelsDir, tier)
		return status, fmt.Errorf("native Qwen setup: package verification failed: %w", err)
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
		if _, err := safeJoinNative(stage, hdr.Name); err != nil {
			return err
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
			if err := rejectNativeSymlinkComponents(stage, target); err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := rejectNativeSymlinkComponents(stage, target); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := rejectNativeSymlinkComponents(stage, target); err != nil {
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
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Allow relative library aliases only when their logical target remains
			// inside the package after the staged tree is promoted into destRoot.
			if err := validateNativeArchiveSymlink(rel, hdr.Linkname); err != nil {
				return err
			}
			if err := rejectNativeSymlinkComponents(stage, target); err != nil {
				return err
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
		info, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("native Qwen setup: refusing top-level symlink %q", name)
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

func validateNativeArchiveSymlink(rel, linkname string) error {
	nativeLink := filepath.FromSlash(linkname)
	if linkname == "" || strings.Contains(linkname, `\`) || path.IsAbs(linkname) ||
		filepath.IsAbs(nativeLink) || filepath.VolumeName(nativeLink) != "" {
		return fmt.Errorf("native Qwen setup: refusing unsafe symlink %q -> %q", rel, linkname)
	}
	target := path.Clean(path.Join(path.Dir(filepath.ToSlash(rel)), linkname))
	if target == ".." || strings.HasPrefix(target, "../") {
		return fmt.Errorf("native Qwen setup: symlink %q -> %q escapes archive root", rel, linkname)
	}
	return nil
}

// rejectNativeSymlinkComponents prevents a later regular-file header from
// following an earlier archive symlink. The final path is checked too because
// os.OpenFile follows an existing symlink at that position.
func rejectNativeSymlinkComponents(root, target string) error {
	if err := requirePathWithinNative(root, target, target); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("native Qwen setup: refusing archive write through symlink %q", current)
		}
	}
	return nil
}

func safeJoinNative(dir, rel string) (string, error) {
	nativeRel := filepath.FromSlash(rel)
	if filepath.IsAbs(nativeRel) || filepath.VolumeName(nativeRel) != "" {
		return "", fmt.Errorf("unsafe absolute path %q", rel)
	}
	target := filepath.Join(dir, nativeRel)
	within, err := filepath.Rel(dir, target)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes archive root", rel)
	}
	return target, nil
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		linkname, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Symlink(linkname, dst)
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

// ResolveNativeURL returns configured URL, env override, or the published
// platform default release asset when available.
func ResolveNativeURL(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_NATIVE_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("QWEN_TTS_NATIVE_URL")); v != "" {
		return v
	}
	url, _ := DefaultNativeRelease()
	return url
}

// ResolveNativeSHA256 returns configured archive checksum, env override, or the
// published platform default digest matching DefaultNativeRelease.
func ResolveNativeSHA256(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("SAMANTHA_QWEN_NATIVE_SHA256")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("QWEN_TTS_NATIVE_SHA256")); v != "" {
		return v
	}
	_, sha := DefaultNativeRelease()
	return sha
}

// PreferNative is always true after cutover (native-only product path).
// Kept for callers/tests; SAMANTHA_QWEN_PREFER_NATIVE no longer disables native.
func PreferNative() bool {
	return true
}

// NativeInstalled is a shorthand for InspectNative(...).Installed with default tier.
func NativeInstalled(modelsDir string) bool {
	return InspectNative(modelsDir, DefaultModelTier).Installed
}
