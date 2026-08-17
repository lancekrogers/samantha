package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

var (
	modelsStatusJSON  bool
	modelsStatusScope scopeFlags
	modelsEnsureScope scopeFlags
	modelsEnsureJSON  bool
	modelsEnsureTier  string
)

// scopeFlags narrows a models command to specific asset kinds. Flags combine as
// a union; no scope flags (or --all) keeps the full default request.
type scopeFlags struct {
	tts     bool
	stt     bool
	vad     bool
	speaker bool
	all     bool
}

func (s *scopeFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&s.tts, "tts", false, "Limit to TTS assets")
	cmd.Flags().BoolVar(&s.stt, "stt", false, "Limit to the configured STT provider's assets")
	cmd.Flags().BoolVar(&s.vad, "vad", false, "Limit to the VAD asset")
	cmd.Flags().BoolVar(&s.speaker, "speaker", false, "Limit to meeting speaker-diarization assets")
	cmd.Flags().BoolVar(&s.all, "all", false, "All asset kinds (same as no scope flags)")
}

// request resolves the flags to the asset request for cfg.
func (s scopeFlags) request(cfg *config.Config) config.AssetRequest {
	if s.all || (!s.tts && !s.stt && !s.vad && !s.speaker) {
		return config.DefaultAssetRequest(cfg)
	}
	return config.ScopedAssetRequest(cfg, config.AssetScope{STT: s.stt, TTS: s.tts, VAD: s.vad, Speaker: s.speaker})
}

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Inspect and manage local model assets",
}

var modelsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show required and installed model assets (read-only, offline)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runModelsStatus(cmd, cfg, config.ModelsDir(), modelsStatusScope.request(cfg), modelsStatusJSON)
	},
}

// qwenTierIDPrefix is the id namespace of the per-tier rows. Shared by the row
// builder and the human printer so the two cannot drift.
const qwenTierIDPrefix = "tts.qwen3.tier."

// qwenStatusRows returns the Qwen3-TTS rows for a TTS-covering status request.
//
// The two kinds of row answer different questions and so have different gates.
// The coarse "tts.qwen3.native" row answers "does the configuration I am
// running need this package, and is it here?" — it belongs to a config that
// actually selects managed qwen. The per-tier rows answer "which model tiers
// are on this disk?", which the Mac app's tier picker has to know *before* the
// user switches tts_provider (spec 55-models §3.2 installs and verifies a tier,
// then saves the key). Gating those on the active provider is what left
// `models status --json --all` with no tier rows at all for a kokoro user.
// The second return value reports whether this configuration actually selects
// managed qwen, which is what tells the human view whether a missing tier is a
// real gap or just an offer.
func qwenStatusRows(cfg *config.Config, modelsDir string) ([]config.AssetStatus, bool) {
	if cfg == nil {
		return nil, false
	}
	native := managedqwen.InspectNative(modelsDir, cfg.QwenTTSModelTier)
	active := strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), managedqwen.ProviderName) &&
		managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel)
	var rows []config.AssetStatus
	if active {
		missing := []string(nil)
		if !native.Installed {
			missing = []string{native.Root}
		}
		name := "Qwen3-TTS native package"
		if native.DefaultTier != "" {
			name += " (" + native.DefaultTier + ")"
		}
		rows = append(rows, config.AssetStatus{
			ID: "tts.qwen3.native", Name: name,
			Provider: managedqwen.ProviderName, Mode: "customvoice", Kind: config.AssetKindTTS,
			Installed: native.Installed, Missing: missing,
		})
	}
	return append(rows, qwenTierStatusRows(native, cfg.QwenTTSNativeURL)...), active
}

// qwenTierStatusRows returns one additive AssetStatus row per known Qwen3-TTS
// model tier (0.6b, 1.7b), reusing the already-computed native inspection so
// the coarse "tts.qwen3.native" row and these per-tier rows never disagree.
// A tier not in native.TiersReady is reported missing; when the package
// itself is not installed at all (both TiersReady/TiersMissing empty), every
// tier's Missing path falls back to the package root, matching the coarse
// row's own behaviour. nativeURL is the configured qwen_tts_native_url, used
// only to compute the fail-closed detail for a missing 1.7b tier.
func qwenTierStatusRows(native managedqwen.NativeStatus, nativeURL string) []config.AssetStatus {
	packageAbsent := len(native.TiersReady) == 0 && len(native.TiersMissing) == 0
	tiers := []string{managedqwen.DefaultModelTier, managedqwen.Tier1_7B}

	rows := make([]config.AssetStatus, 0, len(tiers))
	for _, tier := range tiers {
		installed := false
		for _, ready := range native.TiersReady {
			if ready == tier {
				installed = true
				break
			}
		}
		row := config.AssetStatus{
			ID:        qwenTierIDPrefix + tier,
			Name:      "Qwen3-TTS model tier " + tier,
			Provider:  managedqwen.ProviderName,
			Mode:      "customvoice",
			Kind:      config.AssetKindTTS,
			Installed: installed,
		}
		if !installed {
			if packageAbsent {
				row.Missing = []string{native.Root}
			} else {
				// Flat lab layout (qwen/native.go's tierReady fallback, the
				// path every published release actually uses):
				// <ModelDir>/qwen3-tts-<tier>-f16.gguf — never a per-tier
				// subdirectory. A first cut of this row fabricated
				// ModelDir/<tier>/, a path no layout has ever written to;
				// found by adversarial review.
				row.Missing = []string{filepath.Join(native.ModelDir, fmt.Sprintf("qwen3-tts-%s-f16.gguf", tier))}
			}
			row.Detail = qwenTierFailClosedDetail(nativeURL, tier)
		}
		rows = append(rows, row)
	}
	return rows
}

// qwenTierFailClosedDetail explains why a missing tier cannot simply be
// re-fetched: today only the pinned default release is known to ship 0.6b
// alone, so 1.7b fails closed unless a multi-tier tarball is configured
// (mirrors the fail-fast check in qwen.EnsureNative). Every other case
// returns "" — a plain "missing" is self-explanatory.
func qwenTierFailClosedDetail(configuredURL, tier string) string {
	if tier != managedqwen.Tier1_7B {
		return ""
	}
	defaultURL, _ := managedqwen.DefaultNativeRelease()
	effective := managedqwen.ResolveNativeURL(configuredURL)
	if effective == "" || effective == defaultURL {
		return "not in the pinned release — set qwen_tts_native_url to a multi-tier tarball"
	}
	return ""
}

// missingHint is the human line for a row that is not installed. A Qwen tier
// row names its own --tier flag: a bare `models ensure` installs the tier the
// config points at, so it would silently do nothing for the other one — and
// nothing at all when qwen is not the configured provider.
func missingHint(s config.AssetStatus) string {
	if d := strings.TrimSpace(s.Detail); d != "" {
		return d
	}
	if tier, ok := strings.CutPrefix(s.ID, qwenTierIDPrefix); ok {
		return "missing — run 'samantha models ensure --tts --tier " + tier + "'"
	}
	return "missing — run 'samantha models ensure'"
}

// runModelsStatus resolves the asset manifest for cfg and req and reports each
// asset's installed/missing state under modelsDir. It is read-only and never
// downloads.
func runModelsStatus(cmd *cobra.Command, cfg *config.Config, modelsDir string, req config.AssetRequest, asJSON bool) error {
	manifest, err := config.ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	statuses := manifest.Status(modelsDir)
	qwenActive := false
	if req.NeedTTS {
		var qwenRows []config.AssetStatus
		qwenRows, qwenActive = qwenStatusRows(cfg, modelsDir)
		statuses = append(statuses, qwenRows...)
	}

	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(statuses)
	}
	printModelStatuses(out, modelsDir, statuses, qwenActive)
	return nil
}

// printModelStatuses renders the human table. A Qwen tier row belonging to a
// configuration that does not select qwen is listed but kept out of the missing
// count: it is install state the Models screen asks for, not an asset this
// configuration needs, and counting it would tell a kokoro user their install
// is incomplete no matter what they install.
func printModelStatuses(out io.Writer, modelsDir string, statuses []config.AssetStatus, qwenActive bool) {
	fmt.Fprintf(out, "\n  Model assets (models dir: %s)\n\n", modelsDir)
	if len(statuses) == 0 {
		fmt.Fprintln(out, "  No model assets required for the current configuration.")
		fmt.Fprintln(out)
		return
	}

	missing, offered := 0, 0
	for _, s := range statuses {
		state := "installed"
		if !s.Installed {
			state = missingHint(s)
			if !qwenActive && strings.HasPrefix(s.ID, qwenTierIDPrefix) {
				offered++
			} else {
				missing++
			}
		}
		mode := ""
		if s.Mode != "" {
			mode = "/" + s.Mode
		}
		fmt.Fprintf(out, "  [%s] %s (%s%s) — %s\n", s.Kind, s.Name, s.Provider, mode, state)
	}
	if offered > 0 {
		fmt.Fprintf(out, "\n  %d asset(s), %d missing (plus %d optional Qwen3-TTS tier(s) not installed).\n\n",
			len(statuses), missing, offered)
		return
	}
	fmt.Fprintf(out, "\n  %d asset(s), %d missing.\n\n", len(statuses), missing)
}

var modelsEnsureCmd = &cobra.Command{
	Use:   "ensure",
	Short: "Download any missing model assets for the current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		tier, err := resolveEnsureTier(modelsEnsureTier, modelsEnsureScope.tts)
		if err != nil {
			return err
		}
		req := modelsEnsureScope.request(cfg)

		if modelsEnsureJSON {
			return runModelsEnsureJSON(cmd, cfg, req, tier)
		}

		if err := runModelsEnsure(cmd, cfg, req, config.EnsureRuntimeAssets); err != nil {
			return err
		}
		if tier == "" {
			return nil
		}
		out := cmd.OutOrStdout()
		started := map[string]bool{}
		progress := func(name string, pct float64) {
			if !started[name] {
				started[name] = true
				fmt.Fprintf(out, "  downloading %s ...\n", name)
			}
		}
		if err := ensureQwenTierFn(cmd.Context(), cfg, tier, progress); err != nil {
			return fmt.Errorf("models ensure (tier %s): %w", tier, err)
		}
		fmt.Fprintf(out, "  Tier %s ensured.\n", tier)
		return nil
	},
}

// resolveEnsureTier validates and normalizes the --tier flag. It is only
// valid together with --tts (an explicit tier install without persisting it
// to config, mirroring the TUI's install-then-save flow); empty means "no
// explicit tier install requested".
func resolveEnsureTier(tier string, ttsScope bool) (string, error) {
	if strings.TrimSpace(tier) == "" {
		return "", nil
	}
	if !ttsScope {
		return "", fmt.Errorf("models ensure: --tier applies to --tts")
	}
	return managedqwen.NormalizeModelTier(tier), nil
}

// ensureAssetsFunc matches config.EnsureRuntimeAssets so tests can observe the
// request without downloading.
type ensureAssetsFunc func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(name string, pct float64)) error

// ensureAssetsProgressFunc matches config.EnsureRuntimeAssetsProgress so
// tests can observe per-phase progress without downloading.
type ensureAssetsProgressFunc func(ctx context.Context, cfg *config.Config, req config.AssetRequest, onProgress func(config.AssetProgress)) error

// ensureRuntimeAssetsProgressFn is the models-ensure --json seam; tests swap
// it to observe AssetProgress ticks without downloading.
var ensureRuntimeAssetsProgressFn ensureAssetsProgressFunc = config.EnsureRuntimeAssetsProgress

// ensureQwenTierProgressFn is the AssetProgress-shaped sibling of
// ensureQwenTierFn, used by the --json ensure path (both the persona-tier
// loop and an explicit --tier install) so every tick lands on the same NDJSON
// stream.
var ensureQwenTierProgressFn = config.EnsureQwenTTSTierProgress

// modelsEnsureDone is the single terminal NDJSON line models ensure --json
// emits on success.
type modelsEnsureDone struct {
	Done      bool     `json:"done"`
	Installed []string `json:"installed"`
	Skipped   []string `json:"skipped"`
	ModelsDir string   `json:"models_dir"`
	ElapsedMs int64    `json:"elapsed_ms"`
}

// modelsEnsureFailed is the single terminal NDJSON line models ensure --json
// emits on failure or cancellation.
type modelsEnsureFailed struct {
	Done  bool   `json:"done"`
	Error string `json:"error"`
	Asset string `json:"asset"`
}

// throttleDownloadTicks wraps onProgress so "download"-phase ticks are
// forwarded only when pct has advanced at least one point or 250ms have
// passed since the last tick emitted for that asset. Every other phase
// (start/verify/extract/install/done/skipped) is always forwarded — which
// already guarantees the first and last tick per asset are never dropped,
// since those phases bookend every asset's download ticks.
func throttleDownloadTicks(onProgress func(config.AssetProgress)) func(config.AssetProgress) {
	type tickState struct {
		pct float64
		at  time.Time
	}
	last := map[string]tickState{}
	return func(p config.AssetProgress) {
		if p.Phase != "download" {
			delete(last, p.Asset) // the asset's next download tick always fires
			onProgress(p)
			return
		}
		prev, seen := last[p.Asset]
		if !seen || p.Pct-prev.pct >= 1 || time.Since(prev.at) >= 250*time.Millisecond {
			last[p.Asset] = tickState{pct: p.Pct, at: time.Now()}
			onProgress(p)
		}
	}
}

// runModelsEnsureJSON is the --json form of models ensure: NDJSON progress
// ticks on stdout (nothing else), exactly one terminal summary line, and a
// non-zero exit on failure. It mirrors runModelsEnsure's asset/persona-tier
// flow, plus an explicit tier install when tier is non-empty.
func runModelsEnsureJSON(cmd *cobra.Command, cfg *config.Config, req config.AssetRequest, tier string) error {
	start := time.Now()
	enc := json.NewEncoder(cmd.OutOrStdout())

	var installedOrder, skippedOrder []string
	seenInstalled := map[string]bool{}
	seenSkipped := map[string]bool{}
	var currentAsset string

	track := func(p config.AssetProgress) {
		_ = enc.Encode(p)
		switch p.Phase {
		case "start":
			currentAsset = p.Asset
		case "done":
			currentAsset = ""
			if !seenInstalled[p.Asset] {
				seenInstalled[p.Asset] = true
				installedOrder = append(installedOrder, p.Asset)
			}
		case "skipped":
			currentAsset = ""
			if !seenSkipped[p.Asset] {
				seenSkipped[p.Asset] = true
				skippedOrder = append(skippedOrder, p.Asset)
			}
		}
	}
	onProgress := throttleDownloadTicks(track)

	ctx := cmd.Context()
	runErr := ensureRuntimeAssetsProgressFn(ctx, cfg, req, onProgress)

	if runErr == nil && req.NeedTTS {
		for _, t := range qwenPersonaTiers(cfg) {
			if err := ensureQwenTierProgressFn(ctx, cfg, t, onProgress); err != nil {
				runErr = fmt.Errorf("qwen tier %s for personas: %w", t, err)
				break
			}
		}
	}

	if runErr == nil && tier != "" {
		runErr = ensureQwenTierProgressFn(ctx, cfg, tier, onProgress)
	}

	if runErr != nil {
		errMsg := runErr.Error()
		if errors.Is(runErr, context.Canceled) {
			// A cancelled top-level context can surface wrapped in a "download
			// <asset>: …" prefix depending on which step was in flight; the
			// contract is the bare sentinel string regardless.
			errMsg = context.Canceled.Error()
		}
		_ = enc.Encode(modelsEnsureFailed{Done: false, Error: errMsg, Asset: currentAsset})
		return fmt.Errorf("models ensure: %w", runErr)
	}

	if installedOrder == nil {
		installedOrder = []string{}
	}
	if skippedOrder == nil {
		skippedOrder = []string{}
	}
	return enc.Encode(modelsEnsureDone{
		Done:      true,
		Installed: installedOrder,
		Skipped:   skippedOrder,
		ModelsDir: config.ModelsDirFrom(cfg),
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// runModelsEnsure downloads the missing assets in req for cfg, reporting each
// asset as it begins and a final status line. It returns an actionable error
// naming the failing asset if a download fails.
func runModelsEnsure(cmd *cobra.Command, cfg *config.Config, req config.AssetRequest, ensure ensureAssetsFunc) error {
	out := cmd.OutOrStdout()

	started := map[string]bool{}
	progress := func(name string, pct float64) {
		if !started[name] {
			started[name] = true
			fmt.Fprintf(out, "  downloading %s ...\n", name)
		}
	}
	err := ensure(cmd.Context(), cfg, req, progress)
	if err != nil {
		return fmt.Errorf("models ensure: %w", err)
	}

	// Personas route speech independently of the active TTS provider: a
	// kokoro session with a qwen persona still needs the native package.
	// Ensuring an installed tier is a no-op, so this never re-downloads.
	if req.NeedTTS {
		for _, tier := range qwenPersonaTiers(cfg) {
			if err := ensureQwenTierFn(cmd.Context(), cfg, tier, progress); err != nil {
				return fmt.Errorf("models ensure (qwen tier %s for personas): %w", tier, err)
			}
		}
	}

	if len(started) == 0 {
		fmt.Fprintln(out, "  All required model assets are already present.")
	} else {
		fmt.Fprintf(out, "  Done — %d asset(s) ensured.\n", len(started))
	}
	return nil
}

// ensureQwenTierFn is the persona-tier ensure seam; tests swap it to observe
// tiers without downloading.
var ensureQwenTierFn = config.EnsureQwenTTSTier

// qwenPersonaTiers returns the distinct native tiers persona profiles route
// speech through. Profiles without a pinned tier need the app-level one.
func qwenPersonaTiers(cfg *config.Config) []string {
	profiles, err := personaProfilesFn()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var tiers []string
	for _, p := range profiles {
		if p == nil || !strings.EqualFold(strings.TrimSpace(p.TTS.Provider), managedqwen.ProviderName) {
			continue
		}
		tier := strings.TrimSpace(p.TTS.Tier)
		if tier == "" && cfg != nil {
			tier = strings.TrimSpace(cfg.QwenTTSModelTier)
		}
		tier = managedqwen.NormalizeModelTier(tier)
		if !seen[tier] {
			seen[tier] = true
			tiers = append(tiers, tier)
		}
	}
	sort.Strings(tiers)
	return tiers
}

func init() {
	modelsStatusCmd.Flags().BoolVar(&modelsStatusJSON, "json", false, "Output machine-readable JSON")
	modelsStatusScope.register(modelsStatusCmd)
	modelsEnsureScope.register(modelsEnsureCmd)
	modelsEnsureCmd.Flags().BoolVar(&modelsEnsureJSON, "json", false, "Stream NDJSON progress on stdout, one terminal summary line")
	modelsEnsureCmd.Flags().StringVar(&modelsEnsureTier, "tier", "", "Install a Qwen3-TTS model tier (0.6b|1.7b) without writing config; requires --tts")
	modelsCmd.AddCommand(modelsStatusCmd)
	modelsCmd.AddCommand(modelsEnsureCmd)
	rootCmd.AddCommand(modelsCmd)
}
