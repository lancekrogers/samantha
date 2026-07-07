package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// AssetSchema identifies the asset manifest schema version.
const AssetSchema = "samantha.assets.v1"

// Canonical asset facts shared by the manifest builder and the legacy download
// adapters so URLs and check files have a single source of truth. Checksums for
// these two come from the GitHub release asset digests (sha256).
const (
	sileroVADURL        = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
	sileroVADSHA256     = "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6"
	sileroVADSize       = 643854
	kokoroArchiveURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2"
	kokoroArchiveSHA256 = "c133d26353d776da730870dac7da07dbfc9a5e3bc80cc5e8e83ab6e823be7046"
)

var kokoroCheckFiles = []string{"model.onnx", "voices.bin", "tokens.txt", "espeak-ng-data"}

func sherpaWhisperOfflineURL(model string) string {
	return fmt.Sprintf("https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-%s.tar.bz2", model)
}

// AssetKind classifies what a model asset provides.
type AssetKind string

const (
	AssetKindVAD AssetKind = "vad"
	AssetKindSTT AssetKind = "stt"
	AssetKindTTS AssetKind = "tts"
)

// AssetFile is one downloadable file belonging to an asset. Path is relative to
// the asset's target directory. SHA256 and Size are optional during migration.
type AssetFile struct {
	Path   string `json:"path"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// AssetArchive is a single archive downloaded and extracted into the asset's
// target directory. SHA256 is optional during migration.
type AssetArchive struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}

// Asset describes one model asset. It installs from either a set of individual
// files or a single archive — never both. CheckFiles are the paths, relative to
// the target directory, that must exist for the asset to count as installed.
type Asset struct {
	ID         string        `json:"id"`
	Provider   string        `json:"provider"`
	Mode       string        `json:"mode,omitempty"`
	Kind       AssetKind     `json:"kind"`
	Name       string        `json:"name,omitempty"`       // human-friendly label for progress/diagnostics
	TargetDir  string        `json:"target_dir,omitempty"` // relative to the models dir; empty = models dir root
	Files      []AssetFile   `json:"files,omitempty"`
	Archive    *AssetArchive `json:"archive,omitempty"`
	CheckFiles []string      `json:"check_files,omitempty"` // relative to target dir; required for archives
}

// AssetManifest is a versioned collection of model assets.
type AssetManifest struct {
	Schema string  `json:"schema"`
	Assets []Asset `json:"assets"`
}

// IsArchive reports whether the asset installs from an archive rather than
// individual files.
func (a Asset) IsArchive() bool { return a.Archive != nil }

// Validate checks the structural invariants of a single asset: it must identify
// itself, declare a known kind, and carry exactly one of files or archive (an
// archive additionally requires check files to verify extraction).
func (a Asset) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("asset: missing id")
	}
	if a.Provider == "" {
		return fmt.Errorf("asset %q: missing provider", a.ID)
	}
	switch a.Kind {
	case AssetKindVAD, AssetKindSTT, AssetKindTTS:
	default:
		return fmt.Errorf("asset %q: invalid kind %q", a.ID, a.Kind)
	}

	hasFiles := len(a.Files) > 0
	hasArchive := a.Archive != nil
	if hasFiles == hasArchive {
		return fmt.Errorf("asset %q: must have exactly one of files or archive", a.ID)
	}

	if hasArchive {
		if a.Archive.URL == "" {
			return fmt.Errorf("asset %q: archive missing url", a.ID)
		}
		if len(a.CheckFiles) == 0 {
			return fmt.Errorf("asset %q: archive requires check_files", a.ID)
		}
	}
	for i, f := range a.Files {
		if f.Path == "" {
			return fmt.Errorf("asset %q file %d: missing path", a.ID, i)
		}
		if f.URL == "" {
			return fmt.Errorf("asset %q file %q: missing url", a.ID, f.Path)
		}
	}
	return nil
}

// Validate checks the manifest schema and every asset, rejecting duplicate ids.
func (m AssetManifest) Validate() error {
	if m.Schema != AssetSchema {
		return fmt.Errorf("asset manifest: schema %q, want %q", m.Schema, AssetSchema)
	}
	seen := make(map[string]bool, len(m.Assets))
	for _, a := range m.Assets {
		if err := a.Validate(); err != nil {
			return err
		}
		if seen[a.ID] {
			return fmt.Errorf("asset manifest: duplicate asset id %q", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// ManifestFor builds the asset manifest describing exactly the assets required
// for req under cfg. It is pure metadata — no I/O or network — so status, doctor,
// and the installer can all resolve requirements from one source. STT
// provider/mode selection uses NormalizeSTTWithMode; an unsupported provider
// yields no STT asset (matching the historical download no-op), while an
// unsupported model or invalid stt_mode for a recognized provider returns an
// error naming it.
func ManifestFor(cfg *Config, req AssetRequest) (AssetManifest, error) {
	m := AssetManifest{Schema: AssetSchema}

	if req.NeedVAD {
		m.Assets = append(m.Assets, Asset{
			ID:       "vad.silero.v1",
			Provider: "sherpa",
			Kind:     AssetKindVAD,
			Name:     "silero_vad.onnx",
			Files:    []AssetFile{{Path: "silero_vad.onnx", URL: sileroVADURL, SHA256: sileroVADSHA256, Size: sileroVADSize}},
		})
	}

	if req.NeedSTT {
		if _, ok := NormalizeSTT(cfg.STTProvider); ok {
			norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
			if err != nil {
				return AssetManifest{}, err
			}
			asset, err := sttAsset(cfg, norm)
			if err != nil {
				return AssetManifest{}, err
			}
			if asset != nil {
				m.Assets = append(m.Assets, *asset)
			}
		}
	}

	if req.NeedTTS && ManagedTTS(cfg) {
		m.Assets = append(m.Assets, Asset{
			ID:         "tts.kokoro.multi-lang-v1_0",
			Provider:   "kokoro",
			Kind:       AssetKindTTS,
			Name:       "kokoro-tts",
			Archive:    &AssetArchive{URL: kokoroArchiveURL, SHA256: kokoroArchiveSHA256},
			CheckFiles: kokoroCheckFiles,
		})
	}

	return m, nil
}

// sttAsset resolves the single STT asset for a normalized provider/mode, or nil
// when the recognized provider has no managed asset.
func sttAsset(cfg *Config, norm NormalizedSTT) (*Asset, error) {
	switch {
	case norm.Provider == STTProviderSherpa && norm.Mode == STTModeStreaming:
		s, err := SherpaStreamingModel(cfg.SherpaStreamingModel)
		if err != nil {
			return nil, err
		}
		return &Asset{
			ID:         "stt.sherpa.streaming." + s.DirName,
			Provider:   "sherpa",
			Mode:       "streaming",
			Kind:       AssetKindSTT,
			Name:       s.Name,
			TargetDir:  s.DirName,
			Archive:    &AssetArchive{URL: s.URL, SHA256: s.ArchiveSHA256},
			CheckFiles: s.RequiredFiles(cfg.WhisperQuantized),
		}, nil
	case norm.Provider == STTProviderSherpa && norm.Mode == STTModeOffline:
		model, err := SherpaOfflineWhisperModel(cfg.WhisperModel)
		if err != nil {
			return nil, err
		}
		return &Asset{
			ID:       "stt.sherpa.offline.whisper-" + model,
			Provider: "sherpa",
			Mode:     "offline",
			Kind:     AssetKindSTT,
			Name:     "whisper-" + model,
			Archive:  &AssetArchive{URL: sherpaWhisperOfflineURL(model), SHA256: sherpaWhisperArchiveSHA256[model]},
			CheckFiles: []string{
				model + "-encoder.onnx",
				model + "-decoder.onnx",
			},
		}, nil
	case norm.Provider == STTProviderWhisperCPP:
		a, err := WhisperCPPModelAsset(cfg.WhisperCPPModel)
		if err != nil {
			return nil, err
		}
		return &Asset{
			ID:       "stt.whispercpp." + a.Name,
			Provider: "whispercpp",
			Mode:     "cli",
			Kind:     AssetKindSTT,
			Name:     a.Name,
			Files:    []AssetFile{{Path: filepath.Join("whispercpp", a.Filename), URL: a.URL, SHA256: a.SHA256, Size: a.Size}},
		}, nil
	}
	return nil, nil
}

// ModelFiles returns the individual-file downloads in this manifest (the legacy
// ModelFile view).
func (m AssetManifest) ModelFiles() []ModelFile {
	var files []ModelFile
	for _, a := range m.Assets {
		if a.IsArchive() {
			continue
		}
		for _, f := range a.Files {
			files = append(files, ModelFile{Name: f.Path, URL: f.URL, Size: f.Size, SHA256: f.SHA256})
		}
	}
	return files
}

// AssetScope selects which asset kinds a request should cover.
type AssetScope struct {
	STT bool
	TTS bool
	VAD bool
}

// ScopedAssetRequest returns the asset request for cfg narrowed to the kinds
// selected in scope. Kinds outside the scope are never requested; kinds inside
// it are requested only when cfg needs them, exactly as DefaultAssetRequest
// decides.
func ScopedAssetRequest(cfg *Config, scope AssetScope) AssetRequest {
	// Route through NormalizeSTT so this agrees with ManifestFor on every alias
	// (including the empty default, which normalizes to sherpa-offline); an
	// unrecognized provider needs no STT asset, matching the resolver. A
	// recognized provider with an invalid stt_mode still needs STT, so the
	// mode error surfaces in ManifestFor instead of silently skipping assets.
	_, sttOK := NormalizeSTT(cfg.STTProvider)
	return AssetRequest{
		NeedSTT: scope.STT && sttOK,
		NeedTTS: scope.TTS && ManagedTTS(cfg),
		NeedVAD: scope.VAD && cfg.VADEnabled,
	}
}

// DefaultAssetRequest returns the asset request the default setup/startup path
// uses for cfg — the same provider selection EnsureModels applies.
func DefaultAssetRequest(cfg *Config) AssetRequest {
	return ScopedAssetRequest(cfg, AssetScope{STT: true, TTS: true, VAD: true})
}

// AssetStatus reports the on-disk installation state of one asset.
type AssetStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	Mode      string    `json:"mode,omitempty"`
	Kind      AssetKind `json:"kind"`
	Installed bool      `json:"installed"`
	Missing   []string  `json:"missing,omitempty"`
}

// installPaths returns the absolute paths that must exist for the asset to be
// considered installed under modelsDir.
func (a Asset) installPaths(modelsDir string) []string {
	if a.IsArchive() {
		target := modelsDir
		if a.TargetDir != "" {
			target = filepath.Join(modelsDir, a.TargetDir)
		}
		paths := make([]string, 0, len(a.CheckFiles))
		for _, cf := range a.CheckFiles {
			paths = append(paths, filepath.Join(target, cf))
		}
		return paths
	}
	paths := make([]string, 0, len(a.Files))
	for _, f := range a.Files {
		paths = append(paths, filepath.Join(modelsDir, f.Path))
	}
	return paths
}

func (a Asset) installProblems(modelsDir string) []string {
	if a.IsArchive() {
		target := modelsDir
		if a.TargetDir != "" {
			target = filepath.Join(modelsDir, a.TargetDir)
		}
		var missing []string
		for _, p := range a.installPaths(modelsDir) {
			if !pathExists(p) {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			return missing
		}
		if a.Archive.SHA256 != "" && !archiveInstallMarkerValid(target, a.ID, a.Archive.URL, a.Archive.SHA256, a.CheckFiles) {
			return []string{archiveInstallMarkerPath(target, a.ID)}
		}
		return nil
	}

	var missing []string
	for _, f := range a.Files {
		p := filepath.Join(modelsDir, f.Path)
		if !fileVerified(p, f.Size, f.SHA256) {
			missing = append(missing, p)
		}
	}
	return missing
}

// Status resolves the installation state of every asset in the manifest under
// modelsDir. It only reads the filesystem and never downloads.
func (m AssetManifest) Status(modelsDir string) []AssetStatus {
	out := make([]AssetStatus, 0, len(m.Assets))
	for _, a := range m.Assets {
		st := AssetStatus{ID: a.ID, Name: a.Name, Provider: a.Provider, Mode: a.Mode, Kind: a.Kind}
		st.Missing = a.installProblems(modelsDir)
		st.Installed = len(st.Missing) == 0
		out = append(out, st)
	}
	return out
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ModelArchives returns the archive downloads in this manifest (the legacy
// ModelArchive view), resolving each relative TargetDir under base.
func (m AssetManifest) ModelArchives(base string) []ModelArchive {
	var archives []ModelArchive
	for _, a := range m.Assets {
		if !a.IsArchive() {
			continue
		}
		targetDir := ""
		if a.TargetDir != "" {
			targetDir = filepath.Join(base, a.TargetDir)
		}
		archives = append(archives, ModelArchive{
			ID:         a.ID,
			Name:       a.Name,
			URL:        a.Archive.URL,
			SHA256:     a.Archive.SHA256,
			TargetDir:  targetDir,
			CheckFiles: a.CheckFiles,
		})
	}
	return archives
}
