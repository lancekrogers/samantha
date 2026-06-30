package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AssetSchema identifies the asset manifest schema version.
const AssetSchema = "samantha.assets.v1"

// Canonical asset facts shared by the manifest builder and the legacy download
// adapters so URLs and check files have a single source of truth.
const (
	sileroVADURL     = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
	kokoroArchiveURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2"
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
	URL         string `json:"url"`
	SHA256      string `json:"sha256,omitempty"`
	StripPrefix bool   `json:"strip_prefix"`
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
// provider/mode selection uses NormalizeSTT; an unsupported provider yields no
// STT asset (matching the historical download no-op), while an unsupported model
// for a recognized provider returns an error naming it.
func ManifestFor(cfg *Config, req AssetRequest) (AssetManifest, error) {
	m := AssetManifest{Schema: AssetSchema}

	if req.NeedVAD {
		m.Assets = append(m.Assets, Asset{
			ID:       "vad.silero.v1",
			Provider: "sherpa",
			Kind:     AssetKindVAD,
			Name:     "silero_vad.onnx",
			Files:    []AssetFile{{Path: "silero_vad.onnx", URL: sileroVADURL}},
		})
	}

	if req.NeedSTT {
		if norm, ok := NormalizeSTT(cfg.STTProvider); ok {
			asset, err := sttAsset(cfg, norm)
			if err != nil {
				return AssetManifest{}, err
			}
			if asset != nil {
				m.Assets = append(m.Assets, *asset)
			}
		}
	}

	if req.NeedTTS && strings.EqualFold(cfg.TTSProvider, "kokoro") {
		m.Assets = append(m.Assets, Asset{
			ID:         "tts.kokoro.multi-lang-v1_0",
			Provider:   "kokoro",
			Kind:       AssetKindTTS,
			Name:       "kokoro-tts",
			Archive:    &AssetArchive{URL: kokoroArchiveURL, StripPrefix: true},
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
			Archive:    &AssetArchive{URL: s.URL, StripPrefix: true},
			CheckFiles: s.RequiredFiles(cfg.WhisperQuantized),
		}, nil
	case norm.Provider == STTProviderSherpa && norm.Mode == STTModeOffline:
		model := cfg.WhisperModel
		return &Asset{
			ID:       "stt.sherpa.offline.whisper-" + model,
			Provider: "sherpa",
			Mode:     "offline",
			Kind:     AssetKindSTT,
			Name:     "whisper-" + model,
			Archive:  &AssetArchive{URL: sherpaWhisperOfflineURL(model), StripPrefix: true},
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
			Files:    []AssetFile{{Path: filepath.Join("whispercpp", a.Filename), URL: a.URL}},
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
			files = append(files, ModelFile{Name: f.Path, URL: f.URL, Size: f.Size})
		}
	}
	return files
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
			Name:       a.Name,
			URL:        a.Archive.URL,
			TargetDir:  targetDir,
			CheckFiles: a.CheckFiles,
		})
	}
	return archives
}
