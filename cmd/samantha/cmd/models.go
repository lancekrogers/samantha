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

// runModelsStatus resolves the asset manifest for cfg and req and reports each
// asset's installed/missing state under modelsDir. It is read-only and never
// downloads.
func runModelsStatus(cmd *cobra.Command, cfg *config.Config, modelsDir string, req config.AssetRequest, asJSON bool) error {
	manifest, err := config.ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	statuses := manifest.Status(modelsDir)
	requiredTier := ""
	if req.NeedTTS {
		var qwenRows []config.AssetStatus
		qwenRows, requiredTier = qwenStatusRows(cfg, modelsDir)
		statuses = append(statuses, qwenRows...)
	}

	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(statuses)
	}
	printModelStatuses(out, modelsDir, statuses, requiredTier)
	return nil
}

// printModelStatuses renders the human table. A Qwen tier row this
// configuration would not load is listed but kept out of the missing count: it
// is install state the Models screen asks for, not an asset this configuration
// needs, and counting it would report an install as incomplete no matter what
// the user installs.
func printModelStatuses(out io.Writer, modelsDir string, statuses []config.AssetStatus, requiredTier string) {
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
			state = missingHint(s, requiredTier)
			if offeredRow(s, requiredTier) {
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
