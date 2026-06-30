package config

import "fmt"

// AssetSchema identifies the asset manifest schema version.
const AssetSchema = "samantha.assets.v1"

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
