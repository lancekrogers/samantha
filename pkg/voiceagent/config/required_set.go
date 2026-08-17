package config

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ProtectedPath is one path the cleaner must never offer to delete, with the
// reason it is kept. Callers show this list before any deletion: on
// 2026-08-17 a clean deleted six personas' TTS runtime because nothing ever
// showed what "required" meant.
type ProtectedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// PersonaAssets is one persona and the effective config its voice routes
// through. The persona package imports this one, so the persona overlay cannot
// live here: callers derive Cfg by applying the profile to a copy of the app
// config and pass the result in.
type PersonaAssets struct {
	// ID is the persona id, used in the protected reasons.
	ID string
	// Cfg is the app config with this persona's overrides applied.
	Cfg *Config
}

// RequiredSet is every model asset the current install references: the global
// manifest, every persona's manifest, and every config key that names an
// on-disk asset — whether or not the active mode loads it. CleanCandidates
// subtracts it, so nothing in this set can ever be reported as unused.
type RequiredSet struct {
	// ModelsDir is the root this set was resolved against.
	ModelsDir string
	// Protected lists the kept paths with their reasons, sorted by path. A
	// path with several owners (the qwen native root, typically) appears once
	// per owner so the caller can show every one.
	Protected []ProtectedPath

	// realModelsDir is ModelsDir with symlinks resolved. A configured asset
	// path is routinely spelled through the other one (macOS /tmp vs
	// /private/tmp, a cache dir symlinked to an external volume), and a claim
	// that does not line up with the walk is a claim that protects nothing.
	realModelsDir string

	own ownership
}

// assetClaim is one source's hold on paths under the models dir.
type assetClaim struct {
	// own is every path the claim protects from deletion.
	own []string
	// show is the subset surfaced in Protected — asset roots, not each
	// extracted file or install marker.
	show []string
	// reason explains the hold, e.g. "persona veronica: qwen3-tts tier 0.6b".
	reason string
	// alwaysShow keeps this claim's rows even when an earlier source already
	// protected the same path. Multi-owner assets set it so the Kept list
	// names every owner rather than only the first.
	alwaysShow bool
	// suppressRoot hides unclaimed models-dir-root entries (an installed
	// root-extracting archive whose footprint cannot be enumerated).
	suppressRoot bool
}

// RequiredAssetPaths resolves the union of every asset the install references
// under modelsDir: the global manifest, one manifest per persona (with that
// persona's TTS overrides applied), and every config-referenced asset.
//
// It fails closed. Any manifest that cannot be built — a bad model name, a
// persona whose effective config is missing — returns an error rather than a
// partial set, because a partial required set means real assets get classified
// as unused.
func RequiredAssetPaths(ctx context.Context, cfg *Config, modelsDir string, personas []PersonaAssets) (RequiredSet, error) {
	if err := ctx.Err(); err != nil {
		return RequiredSet{}, err
	}
	if cfg == nil {
		return RequiredSet{}, fmt.Errorf("required assets: nil config")
	}
	modelsDir = filepath.Clean(modelsDir)
	set := RequiredSet{ModelsDir: modelsDir, realModelsDir: resolveSymlinks(modelsDir), own: newOwnership()}

	global, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
	if err != nil {
		return RequiredSet{}, fmt.Errorf("global manifest: %w", err)
	}
	set.addManifest(global, "global ")

	for _, p := range personas {
		if p.Cfg == nil {
			return RequiredSet{}, fmt.Errorf("persona %q: no effective config", p.ID)
		}
		manifest, err := ManifestFor(p.Cfg, DefaultAssetRequest(p.Cfg))
		if err != nil {
			return RequiredSet{}, fmt.Errorf("persona %q manifest: %w", p.ID, err)
		}
		prefix := fmt.Sprintf("persona %s: ", personaLabel(p.ID))
		set.addManifest(manifest, prefix)
		// A persona's voice keys name assets the manifest does not carry —
		// the thewh1teagle kokoro pack KokoroDir prefers, an external qwen
		// tree. Computing these from the global config alone is how a
		// kokoro-speaking persona lost its pack while the app spoke qwen.
		set.addAll(ttsConfigClaims(p.Cfg, modelsDir), prefix)
	}

	claims, err := configReferencedClaims(cfg, modelsDir)
	if err != nil {
		return RequiredSet{}, err
	}
	set.addAll(claims, "config ")
	set.addAll(qwenNativeClaims(cfg, personas, modelsDir), "")
	set.sortProtected()
	return set, nil
}

// CleanCandidates lists the paths under the models dir that no required asset
// claims. It only reads the filesystem — it never deletes, never follows
// symlinks, and never reports a path outside the models dir.
// Candidates are sorted by models-dir-relative path so output, fixtures, and
// the plan id are stable across runs.
func (rs RequiredSet) CleanCandidates(ctx context.Context) ([]CleanCandidate, error) {
	candidates := []CleanCandidate{}
	if err := collectCandidates(ctx, rs.ModelsDir, rs.ModelsDir, rs.own, rs.own.suppressRoot, &candidates); err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Rel < candidates[j].Rel })
	return candidates, nil
}

// addManifest claims every asset in m, prefixing each asset's reason with the
// source (e.g. "global " or "persona veronica: ").
func (rs *RequiredSet) addManifest(m AssetManifest, reasonPrefix string) {
	for _, a := range m.Assets {
		own, suppressRoot := a.ownedPaths(rs.ModelsDir)
		rs.add(assetClaim{
			own:          own,
			show:         a.displayPaths(rs.ModelsDir),
			reason:       reasonPrefix + assetReason(a),
			suppressRoot: suppressRoot,
		})
	}
}

// addAll records claims under one reason prefix ("config ", "persona ada: ").
func (rs *RequiredSet) addAll(claims []assetClaim, reasonPrefix string) {
	for _, c := range claims {
		c.reason = reasonPrefix + c.reason
		rs.add(c)
	}
}

// add records a claim: its paths become un-deletable, and its display paths
// become Protected rows. A path already protected by an earlier source is not
// repeated unless the claim asks for it, so every persona sharing the global
// STT model does not produce a row each.
//
// Ownership covers referenced paths whether or not they exist — an asset
// mid-download must not become deletable — but only paths actually on disk are
// listed as kept, because "Kept (N)" answers what this machine still has.
func (rs *RequiredSet) add(c assetClaim) {
	shown := map[string]bool{}
	for _, raw := range c.show {
		p, ok := rs.underModelsDir(raw)
		if !ok || !pathExists(p) {
			continue
		}
		if rs.own.has(p) && !c.alwaysShow {
			continue
		}
		if shown[p] {
			continue
		}
		shown[p] = true
		rs.Protected = append(rs.Protected, ProtectedPath{Path: p, Reason: c.reason})
	}
	for _, raw := range c.own {
		p, ok := rs.underModelsDir(raw)
		if !ok {
			continue
		}
		rs.own.add(p, rs.ModelsDir)
	}
	rs.own.suppressRoot = rs.own.suppressRoot || c.suppressRoot
}

// underModelsDir reports whether p sits inside the models dir, and returns it
// spelled the way the walk will spell it. A configured path may name the same
// file through the models dir's symlink target, so both spellings are tried;
// paths genuinely elsewhere on disk are never candidates, so protecting them
// would only add noise to the kept list.
func (rs RequiredSet) underModelsDir(p string) (string, bool) {
	spellings := []string{p}
	if resolved := resolveSymlinks(p); resolved != p {
		spellings = append(spellings, resolved)
	}
	for _, base := range []string{rs.ModelsDir, rs.realModelsDir} {
		for _, spelling := range spellings {
			if base == "" {
				continue
			}
			rel, err := filepath.Rel(base, spelling)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return filepath.Join(rs.ModelsDir, rel), true
		}
	}
	return "", false
}

// resolveSymlinks returns path with symlinks resolved. A path that does not
// exist yet is resolved as far as its deepest existing ancestor, so a claim on
// an asset still downloading still lines up with the walk.
func resolveSymlinks(path string) string {
	suffix := ""
	for current := filepath.Clean(path); ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = filepath.Join(filepath.Base(current), suffix)
		current = parent
	}
}

// sortProtected orders the kept list by path, then reason, so output is stable
// across runs and diffable in fixtures.
func (rs *RequiredSet) sortProtected() {
	sort.Slice(rs.Protected, func(i, j int) bool {
		if rs.Protected[i].Path == rs.Protected[j].Path {
			return rs.Protected[i].Reason < rs.Protected[j].Reason
		}
		return rs.Protected[i].Path < rs.Protected[j].Path
	})
}

// displayPaths are the headline paths for the kept list: the extraction target
// for a directory archive, otherwise the asset's own install paths (never the
// install markers or the rest of a root archive's footprint).
func (a Asset) displayPaths(modelsDir string) []string {
	if a.IsArchive() && a.TargetDir != "" {
		return []string{filepath.Join(modelsDir, a.TargetDir)}
	}
	return a.installPaths(modelsDir)
}

// personaLabel keeps a reason readable when a caller supplies a persona with
// no id (nothing on disk does, but a reason must never read "persona : …").
func personaLabel(id string) string {
	if strings.TrimSpace(id) == "" {
		return "(unnamed)"
	}
	return id
}

// assetReason names the config keys that make one asset required, in the words
// a user can act on.
func assetReason(a Asset) string {
	switch a.Kind {
	case AssetKindVAD:
		return "vad_enabled"
	case AssetKindSTT:
		if a.Mode != "" {
			return fmt.Sprintf("stt_provider %s (%s)", a.Provider, a.Mode)
		}
		return "stt_provider " + a.Provider
	case AssetKindTTS:
		return "tts_provider " + a.Provider
	case AssetKindSpeaker:
		return "speaker.enabled: " + a.Name
	}
	return a.ID
}
